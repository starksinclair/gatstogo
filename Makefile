# Teammate shortcuts. Prefer these over remembering long docker/gtg commands.
.PHONY: help env up down logs migrate status seed gtg-build

help:
	@echo "GatsToGo local / DB commands"
	@echo ""
	@echo "  make env       Create .env from .env.example if missing"
	@echo "  make up        docker compose up --build (migrate + seed + app)"
	@echo "  make down      docker compose down"
	@echo "  make logs      Follow app logs"
	@echo "  make migrate   gtg migrate (Neon/host; needs MIGRATE_DATABASE_URL)"
	@echo "  make status    gtg migrate status"
	@echo "  make seed      gtg db:seed (dev/staging only)"
	@echo ""
	@echo "See docs/DATABASE.md for Neon + Cloud Run env vars."

env:
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "Created .env from .env.example"; \
		echo "Set PAYSTACK_SECRET_KEY (Paystack test key) before buying gas."; \
	else \
		echo ".env already exists"; \
	fi

up: env
	docker compose up --build

down:
	docker compose down

logs:
	docker compose logs -f app

gtg-build:
	go build -o bin/gtg ./cmd/gtg

migrate: gtg-build
	./bin/gtg migrate

status: gtg-build
	./bin/gtg migrate status

seed: gtg-build
	./bin/gtg db:seed
