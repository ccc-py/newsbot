INS_PATH = /srv/newsbot

newsbot: main.go
	go build

clean:
	rm -f newsbot

config:
	@echo "Copy newsbot.service"
	@sudo cp ./newsbot.service /lib/systemd/system
	@echo "Enable newsbot service"
	@sudo systemctl enable newsbot.service

install:
	@systemctl stop newsbot.service
	@echo "Installation path" $(INS_PATH)
	@mkdir -p $(INS_PATH)
	@cp ./newsbot $(INS_PATH)
	@cp ./config.tech.yml $(INS_PATH)
	@cp ./config.pm25.yml $(INS_PATH)
	@cp ./translate.yml $(INS_PATH)
	@systemctl start newsbot.service

start:
	systemctl start newsbot.service

stop:
	systemctl stop newsbot.service

status:
	systemctl status newsbot.service

restart:
	systemctl restart newsbot.service
