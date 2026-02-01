# Copyright 2021 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd/migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -mod=vendor -o migrate ./cmd/migrate

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/migrate .
ENTRYPOINT ["./migrate"]
