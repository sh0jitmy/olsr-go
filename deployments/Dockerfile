# Copyright 2026 The olsrd-go Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build Stage
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o olsrd-go ./cmd/olsrd/

# Run Stage
FROM alpine:3.21
RUN apk add --no-cache iproute2 iputils
WORKDIR /
COPY --from=builder /app/olsrd-go /usr/local/bin/olsrd-go
ENTRYPOINT ["/usr/local/bin/olsrd-go"]
