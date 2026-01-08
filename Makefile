.PHONY: build test

REGISTRY=semaphoreci/controller
LATEST_VERSION=$(shell git tag | sort --version-sort | tail -n 1)

APP_NAME=agent-k8s-controller
MONOREPO_TMP_DIR?=/tmp/monorepo
SECURITY_TOOLBOX_TMP_DIR?=$(MONOREPO_TMP_DIR)/security-toolbox
SECURITY_TOOLBOX_BRANCH ?= main
APP_DIRECTORY ?= /app
SECURITY_SCANNERS=vuln,secret,misconfig

check.prepare:
	rm -rf $(MONOREPO_TMP_DIR)
	git clone --depth 1 --filter=blob:none --sparse https://github.com/semaphoreio/semaphore $(MONOREPO_TMP_DIR) && \
		cd $(MONOREPO_TMP_DIR) && \
		git config core.sparseCheckout true && \
		git sparse-checkout init --cone && \
		git sparse-checkout set security-toolbox && \
		git checkout main && cd -


check.static: check.prepare
	docker run -it -v $$(pwd):$(APP_DIRECTORY) \
		-v $(SECURITY_TOOLBOX_TMP_DIR):$(SECURITY_TOOLBOX_TMP_DIR) \
		registry.semaphoreci.com/ruby:3 \
		bash -c 'cd $(APP_DIRECTORY) && $(SECURITY_TOOLBOX_TMP_DIR)/code --language go -d'

check.deps: check.prepare
	docker run -it -v $$(pwd):$(APP_DIRECTORY) \
		-v $(SECURITY_TOOLBOX_TMP_DIR):$(SECURITY_TOOLBOX_TMP_DIR) \
		registry.semaphoreci.com/ruby:3 \
		bash -c 'cd $(APP_DIRECTORY) && $(SECURITY_TOOLBOX_TMP_DIR)/dependencies --language go -d'

check.docker: check.prepare
	docker run -it -v $$(pwd):$(APP_DIRECTORY) \
		-v $(SECURITY_TOOLBOX_TMP_DIR):$(SECURITY_TOOLBOX_TMP_DIR) \
		-v /var/run/docker.sock:/var/run/docker.sock \
		registry.semaphoreci.com/ruby:3 \
		bash -c 'cd $(APP_DIRECTORY) && $(SECURITY_TOOLBOX_TMP_DIR)/docker -d --image $(REGISTRY):latest --scanners $(SECURITY_SCANNERS)'

check.generate-report: check.prepare
	docker run -it \
		-v $$(pwd):/app \
		-v $(SECURITY_TOOLBOX_TMP_DIR):$(SECURITY_TOOLBOX_TMP_DIR) \
		registry.semaphoreci.com/ruby:3 \
		bash -c 'cd $(APP_DIRECTORY) && $(SECURITY_TOOLBOX_TMP_DIR)/report --service-name "[$(CHECK_TYPE)] $(APP_NAME)"'

check.generate-global-report: check.prepare
	docker run -it \
		-v $$(pwd):/app \
		-v $(SECURITY_TOOLBOX_TMP_DIR):$(SECURITY_TOOLBOX_TMP_DIR) \
		registry.semaphoreci.com/ruby:3 \
		bash -c 'cd $(APP_DIRECTORY) && $(SECURITY_TOOLBOX_TMP_DIR)/global-report -i reports -o out'


lint:
	revive -formatter friendly -config lint.toml ./...

test:
	docker compose run --rm app gotestsum --format short-verbose --junitfile junit-report.xml --packages="./..." -- -p 1

build:
	rm -rf build
	env GOOS=linux go build -o build/controller main.go

docker.build: build
	docker build -t $(REGISTRY):latest .

docker.push:
	@if [ -z "$(LATEST_VERSION)" ]; then \
		docker push $(REGISTRY):latest; \
	else \
		docker tag $(REGISTRY):latest $(REGISTRY):$(LATEST_VERSION); \
		docker push $(REGISTRY):$(LATEST_VERSION); \
		docker push $(REGISTRY):latest; \
	fi
