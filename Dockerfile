# For contributors without Nix. The published image is built with `nix build .#image`.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /registry-ui .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /registry-ui /registry-ui
EXPOSE 8080
HEALTHCHECK CMD ["/registry-ui", "healthcheck"]
ENTRYPOINT ["/registry-ui"]
