.PHONY: deploy
.PHONY: functional-test

deploy:
	docker compose build --pull authoriser logkeymanager logservice accountmanager tokenhandler nginx
	@echo "DEVELOPMENT DEPLOY: deleting all PostgreSQL logs/API keys."
	docker compose stop postgres_migrate
	docker compose rm --stop --force --volumes postgres
	docker compose up -d --wait postgres
	docker compose run --rm postgres_migrate
	docker compose up -d --no-deps --force-recreate authoriser logkeymanager logservice accountmanager tokenhandler
	docker compose up -d --no-deps --force-recreate nginx

functional-test:
	go test ./test/functional
