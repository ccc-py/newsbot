package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/translate"
	"github.com/abadojack/whatlanggo"
	strip "github.com/grokify/html-strip-tags-go"
	"github.com/spf13/viper"
	"golang.org/x/text/language"
)

type transType struct {
	target  string
	replace string
}

type newsApp struct {
	group          string
	exclusive      string
	emmaToken      string
	consumerKey    string
	consumerSecret string
	refreshToken   string
	newskey        string
	interval       int32
	phase          int32
	keywords       []string
	blockedWords   []string
	trans          []transType
}

type emmaTokenType struct {
	AccessToken  string `json:"access_token"`
	ExpiredIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
}

func (app *newsApp) checkBlockedWords(input string) bool {
	for i := 0; i < len(app.blockedWords); i++ {
		if len(app.blockedWords[i]) == 0 {
			continue
		}

		log.Println("checkBlockedWords:", app.blockedWords[i])
		if strings.Contains(input, app.blockedWords[i]) {
			return true
		}
	}

	return false
}

func (app *newsApp) readConfig(confFile string) {
	viper.SetConfigName(confFile) // 指定 config 的檔名
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	e := viper.ReadInConfig()
	if e != nil {
		log.Fatal("ReadInConfig:", e)
	}
	viper.AutomaticEnv()

	keyword := viper.GetString("keyword")
	app.group = viper.GetString("group")
	app.keywords = strings.Split(keyword, ",")
	app.exclusive = viper.GetString("exclusive")
	blockedWords := viper.GetString("blocked_words")
	app.blockedWords = strings.Split(blockedWords, ",")
	app.emmaToken = viper.GetString("emma_token")
	app.consumerKey = viper.GetString("consumer_key")
	app.consumerSecret = viper.GetString("consumer_secret")
	app.refreshToken = viper.GetString("emma_refresh_token")
	app.interval = viper.GetInt32("interval")
	app.phase = viper.GetInt32("phase")

	viper.SetConfigName("translate")
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	e = viper.ReadInConfig()
	if e != nil {
		log.Fatal("ReadInConfig:", e)
	}
	viper.AutomaticEnv()
	t := viper.GetStringSlice("replace")
	for _, v := range t {
		s := string(v)
		a := strings.Split(s, ",")
		tr := transType{a[0], a[1]}
		app.trans = append(app.trans, tr)
	}
}

func googleTranslate(source string) (string, error) {
	ctx := context.Background()
	client, err := translate.NewClient(ctx)
	if err != nil {
		return "", err
	}
	translations, err := client.Translate(ctx,
		[]string{source}, language.TraditionalChinese,
		&translate.Options{
			Source: language.English,
			Format: translate.Text,
		})
	if err != nil {
		return "", err
	}

	return translations[0].Text, nil
}

func getQueryDate() string {
	loc, _ := time.LoadLocation("UTC")
	t := time.Now().In(loc)
	y := t.Add(time.Hour * -12)
	s := url.QueryEscape(y.Format(time.RFC3339))

	return s
}

func genSearchURL(newskey string, q string, lang string, exclusive string, page int) string {
	// lang: ar de en es fr he it nl no pt ru se ud zh
	ex := url.QueryEscape(exclusive)
	s := "https://newsapi.org/v2/everything?q=" + url.QueryEscape(q) + "&pageSize=100&from=" + getQueryDate() + "&page=" + strconv.Itoa(page) + "&language=" + lang + "&apiKey=" + newskey + "&excludeDomains=" + ex
	log.Println(s)
	return s
}

type newsSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type newsArticle struct {
	Source         newsSource `json:"source"`
	Author         string     `json:"author"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	URL            string     `json:"url"`
	URLToImage     string     `json:"urlToImage"`
	PublishedAt    string     `json:"publishedAt"`
	Content        string     `json:"content"`
	Hash           string     `json:"hash"`
	Posted         bool       `json:"posted"`
	Keyword        string     `json:"keyword"`
	Embeddings     []float32  `json:"embeddings"`
	Recommendation string     `json:"recommendation"`
}

type newsArticleArray []newsArticle

/*
{"status":"error","code":"rateLimited","message":"You have made too many requests recently. Developer accounts are limited to 500 requests over a 24 hour period (250 requests available every 12 hours). Please upgrade to a paid plan if you need more requests."}
*/
type newsHeader struct {
	Status       string           `json:"status"`
	TotalResults int              `json:"totalResults"`
	NewsArticles newsArticleArray `json:"articles"`
	Code         string           `json:"code"`
	Message      string           `json:"message"`
	Description  string           `json:"description"`
}

func (a newsArticleArray) Len() int {
	return len(a)
}

func (a newsArticleArray) Less(i, j int) bool {
	if a[i].PublishedAt > a[j].PublishedAt {
		return true
	}
	return false
}

func (a newsArticleArray) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func cosine(a []float64, b []float64) (cosine float64, err error) {
	count := 0
	lenA := len(a)
	lenB := len(b)
	if lenA > lenB {
		count = lenA
	} else {
		count = lenB
	}
	sumA := 0.0
	s1 := 0.0
	s2 := 0.0
	for k := 0; k < count; k++ {
		if k >= lenA {
			s2 += math.Pow(b[k], 2)
			continue
		}
		if k >= lenB {
			s1 += math.Pow(a[k], 2)
			continue
		}
		sumA += a[k] * b[k]
		s1 += math.Pow(a[k], 2)
		s2 += math.Pow(b[k], 2)
	}
	if s1 == 0 || s2 == 0 {
		return 0.0, errors.New("Vectors should not be null (all zeros)")
	}
	return sumA / (math.Sqrt(s1) * math.Sqrt(s2)), nil
}

func (app *newsApp) httpPostEmma(content string) {
	e := app.doRefreshToken()
	if e != nil {
		log.Println("doRefreshToken:", e)
		return
	}
	encCnt := url.QueryEscape(content)
	postStr := []byte("access_token=" + app.emmaToken + "&content=" + encCnt + "&format=json")
	postURL := "https://emma.pixnet.cc/topic/" + app.group + "/comment"
	req, e := http.NewRequest("POST", postURL, bytes.NewBuffer(postStr))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, e := client.Do(req)
	if e != nil {
		log.Println("httpPostEmma:", e)
	}
	defer resp.Body.Close()

	/*
		statusCode := resp.StatusCode
		header := resp.Header
		body, _ := ioutil.ReadAll(resp.Body)
		log.Println(string(body))
		log.Println(statusCode)
		log.Println(header)
	*/
	b, _ := ioutil.ReadAll(resp.Body)
	log.Println("post status:", resp.StatusCode)
	log.Println(string(b))
}

func getHasString(s string) string {
	s = "k07382" + s
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}

func (app *newsApp) getDbName() string {
	return "./" + app.group + "-news.json"
}

func (app *newsApp) getArchiveName() string {
	return "./archive-" + app.group + "-news.json"
}

func readDbJSON(fileName string) newsArticleArray {
	b, e := ioutil.ReadFile(fileName)
	var ns newsArticleArray
	if e != nil {
		log.Println(e)
		return ns
	}

	e = json.Unmarshal(b, &ns)
	if e != nil {
		log.Fatalln(e)
	}
	return ns
}

func (app *newsApp) readNewsDb() newsArticleArray {
	return readDbJSON(app.getDbName())
}

func (app *newsApp) readNewsArchive() newsArticleArray {
	return readDbJSON(app.getArchiveName())
}

func (app *newsApp) writeDbJSON(fileName string, ns newsArticleArray) {
	b, e := json.Marshal(ns)
	if e != nil {
		log.Fatalln(e)
	}
	e = ioutil.WriteFile(fileName, b, 0644)
	if e != nil {
		log.Fatalln(e)
	}
}

func (app *newsApp) writeNewsDb(ns newsArticleArray) {
	app.writeDbJSON(app.getDbName(), ns)
}

func (app *newsApp) writeNewsArchive(ns newsArticleArray) {
	app.writeDbJSON(app.getArchiveName(), ns)
}

func (app *newsApp) newsQuery(q string, lang string) newsHeader {
	q = url.QueryEscape(q)
	resp, e := http.Get(genSearchURL(app.newskey, q, lang, app.exclusive, 1))
	if e != nil {
		log.Fatalln(e)
	}

	defer resp.Body.Close()

	body, e := ioutil.ReadAll(resp.Body)
	if e != nil {
		log.Fatalln(e)
	}

	var n newsHeader
	e = json.Unmarshal(body, &n)
	if e != nil {
		log.Fatalln(e)
	}

	if n.Status == "error" {
		log.Println("newsapi error:", n.Message)
		return n
	}

	return n
}

func doInsertNews(na *newsArticleArray, n *newsArticleArray, keyword string) {
	for i := 0; i < len(*n); i++ {
		article := (*n)[i]
		article.Hash = getHasString(article.Description)
		article.Posted = false
		article.Keyword = keyword

		hit := false
		for j := 0; j < len(*na); j++ {
			if (*na)[j].Hash == article.Hash {
				hit = true
				break
			}
		}

		if !hit {
			*na = append(*na, article)
		}
	}
}

func (app *newsApp) insertNews(na *newsArticleArray, q string, lang string) {
	n := app.newsQuery(q, lang)
	doInsertNews(na, &n.NewsArticles, q)
}

func (app *newsApp) randomNews(na *newsArticleArray) {
	if len(app.keywords) <= 0 {
		return
	}
	idx := rand.Int31n(int32(len(app.keywords)))
	app.insertNews(na, app.keywords[idx], "en")
}

func (app *newsApp) doRefreshToken() error {
	URL := "https://emma.pixnet.cc/oauth2/grant?grant_type=refresh_token&refresh_token=" + app.refreshToken + "&client_id=" + app.consumerKey + "&client_secret=" + app.consumerSecret
	resp, e := http.Get(URL)
	if e != nil {
		log.Println(e)
		return e
	}

	defer resp.Body.Close()

	body, e := ioutil.ReadAll(resp.Body)
	if e != nil {
		log.Println(e)
		return e
	}

	var emmaToken emmaTokenType
	e = json.Unmarshal(body, &emmaToken)
	if e != nil {
		return e
	}
	app.emmaToken = emmaToken.AccessToken

	return nil
}

func removeSpace(in string) string {
	out := ""
	for i := 0; i < len(in); i++ {
		if in[i] != ' ' {
			out += string(in[i])
		}
	}

	return out
}

func (na *newsArticleArray) selPostIdx() int32 {
	idx, counter := int32(0), 0
	for true {
		idx = rand.Int31n(int32(len(*na)))
		if (*na)[idx].Posted == false {
			break
		}
		counter++
		if counter > 50 {
			idx = 0
			break
		}
	}
	return idx
}

func (app *newsApp) run() {
	phase := int32(0)
	for true {
		na := app.readNewsDb()
		if phase == 0 {
			app.randomNews(&na)
			phase = app.phase
		}
		phase--
		sort.Sort(na)
		if len(na) > 0 {
			idx := na.selPostIdx()
			s := na[idx].Description
			tag := na[idx].Keyword
			a := strings.Split(s, "…")
			output := ""
			info := whatlanggo.Detect(a[0])
			if info.Lang.String() != "han" {
				var e error
				output, e = googleTranslate(a[0])
				if e != nil {
					output = a[0] // reset
				} else {
					output += "..."
				}
			} else {
				output = a[0]
			}

			title := ""
			info = whatlanggo.Detect(na[idx].Title)
			if info.Lang.String() != "han" {
				var e error
				title, e = googleTranslate(na[idx].Title)
				if e != nil {
					title = na[idx].Title
				}
			} else {
				title = na[idx].Title
			}
			log.Println("post:", title+" --> "+output)
			/* check if the title in blocked words or not, if in blocked words, drop it */
			out := title + "\n\n" + output
			if len(tag) > 0 {
				out += (" #" + removeSpace(tag))
			}
			re := regexp.MustCompile(`<\/ [A-z]*>`)
			out = re.ReplaceAllString(out, "")
			out = strip.StripTags(out)
			for i := 0; i < len(app.trans); i++ {
				log.Println("check:", app.trans[i].target, " --> ", app.trans[i].replace)
				out = strings.Replace(out, app.trans[i].target, app.trans[i].replace, -1)
			}
			out += ("\n" + na[idx].URL)
			if app.checkBlockedWords(out) {
				continue /* matched blovked words, find next article */
			}
			app.httpPostEmma(out)
			if len(na) > 500 {
				archive := app.readNewsArchive()
				archive = append(na[500:], archive...)
				app.writeNewsArchive(archive)
				na = na[:500]
			}
			na[idx].Posted = true
			app.writeNewsDb(na)
			m := rand.Int31n(app.interval)
			time.Sleep(time.Duration(m) * time.Minute)
		} else {
			time.Sleep(time.Minute)
		}
	}
}

func main() {
	var myapp newsApp
	var pm25app newsApp

	rand.Seed(time.Now().UnixNano())
	myapp.readConfig("config.tech")
	pm25app.readConfig("config.pm25")

	go myapp.run()
	go pm25app.run()

	for true {
		time.Sleep(time.Minute)
	}
}
