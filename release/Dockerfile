# The image holds the program and nothing else, for COPY --from=ghcr.io/kitecorp/proxyfile:<version> /proxyfile.
# The build context is dist/, which scripts/build-release.sh fills.
FROM scratch
ARG TARGETARCH
COPY proxyfile-linux-${TARGETARCH} /proxyfile
LABEL org.opencontainers.image.source="https://github.com/kitecorp/proxyfile" \
      org.opencontainers.image.licenses="Apache-2.0"
ENTRYPOINT ["/proxyfile"]
