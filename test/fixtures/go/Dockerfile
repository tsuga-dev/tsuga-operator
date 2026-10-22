FROM golang:1.25 AS build
WORKDIR /src
COPY main.go .
# Not stripped: the eBPF agent finds its probes through the symbol table.
RUN CGO_ENABLED=0 go build -o /out/app main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app"]
