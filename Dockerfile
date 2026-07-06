FROM gcr.io/distroless/static-debian11:nonroot

ENTRYPOINT ["/plaid-lint"]
COPY plaid-lint /
