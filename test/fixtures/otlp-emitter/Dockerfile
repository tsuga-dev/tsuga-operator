FROM golang:1.26 AS build
WORKDIR /src
COPY test/fixtures/otlp-emitter/go.mod test/fixtures/otlp-emitter/go.sum ./
RUN go mod download
COPY test/fixtures/otlp-emitter/ .
RUN CGO_ENABLED=0 go build -o /out/otlp-emitter .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/otlp-emitter /otlp-emitter
USER 65532:65532
ENTRYPOINT ["/otlp-emitter"]
