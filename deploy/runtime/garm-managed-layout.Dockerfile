# Disposable hosted compatibility probe only. No provider, config or credentials.
# STOCK_IMAGE must be the lab's pinned stock reference, never a moving tag.
ARG STOCK_IMAGE
FROM ${STOCK_IMAGE} AS stock
FROM stock AS managed-layout
# A real independent file, not a symlink/hardlink; retain unmodified binary bytes.
COPY --from=stock --chmod=0555 /bin/garm /usr/bin/garm
USER 65530:65530
WORKDIR /
ENTRYPOINT []
CMD []
