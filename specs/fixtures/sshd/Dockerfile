FROM docker.io/library/alpine:3
RUN apk add --no-cache openssh openssh-server shadow \
    && adduser -D -s /bin/sh ops \
    && echo "ops:$(head -c 24 /dev/urandom | base64)" | chpasswd
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
