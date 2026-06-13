FROM alpine

RUN apk add --no-cache qemu-img

ADD build/virtmanager-container-disk-base/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
