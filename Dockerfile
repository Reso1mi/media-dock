FROM golang:1.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/media-dock ./cmd/media-dock

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/media-dock /media-dock
EXPOSE 8080
ENTRYPOINT ["/media-dock"]
