# Copyright 2021 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

VERSION = $(shell git describe --always --tags)
IMAGE = urlstat
BINARY = urlstat
TARGET = -o $(BINARY)
BUILD_FLAGS = $(TARGET) -mod=vendor

all:
	go build $(BUILD_FLAGS)
run:
	./$(BINARY) -s
build:
	CGO_ENABLED=0 GOOS=linux go build $(BUILD_FLAGS)
	docker build -t $(IMAGE):latest .
up:
	docker-compose up -d
down:
	docker-compose down
clean:
	rm -rf $(BINARY)
	docker rmi -f $(shell docker images -f "dangling=true" -q) 2> /dev/null; true
	docker rmi -f $(IMAGE):latest $(IMAGE):$(VERSION) 2> /dev/null; true