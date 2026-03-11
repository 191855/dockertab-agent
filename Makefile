.PHONY: setup up down logs

# Detect the host's LAN IP (works on macOS and Linux)
HOST_IP := $(shell ipconfig getifaddr en0 2>/dev/null || ip -4 route get 1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($$i=="src"){print $$(i+1); exit}}')

## setup: write .env with the auto-detected LAN IP
setup:
	@if [ -z "$(HOST_IP)" ]; then echo "ERROR: could not detect LAN IP — set DOCKERTAB_HOST manually"; exit 1; fi
	@echo "DOCKERTAB_HOST=$(HOST_IP)" > .env
	@echo "✓ .env created — DOCKERTAB_HOST=$(HOST_IP)"

## up: setup + start containers
up: setup
	docker compose up -d

## down: stop containers
down:
	docker compose down

## logs: tail container logs
logs:
	docker compose logs -f
