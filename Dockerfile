FROM golang:1.12-rc-alpine as build

RUN apk add --no-cache git=2.20.1-r0

WORKDIR /src
COPY go.mod go.sum .env *.go iceservers.txt ./
COPY backend-protobuf/go ./backend-protobuf/go
RUN go get -d -v ./...
RUN CGO_ENABLED=0 go build -ldflags "-s -w"

FROM scratch

COPY --from=build /src/iceservers.txt /iceservers.txt
COPY --from=build /src/webrtc /webrtc
COPY --from=build /src/.env /.env

ENTRYPOINT ["/webrtc"]
