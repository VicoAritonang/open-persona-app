#!/bin/sh
# Installs openpersona-cloud on an Amazon Linux 2023 or Ubuntu EC2 instance
# (t3.small is enough). Build the binary on your machine first, from backend/:
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o deploy/aws/ec2/openpersona-cloud ./cmd/openpersona-cloud
# copy it, this directory, and your env file to the instance, then run: sudo ./install.sh
set -eu
cd "$(dirname "$0")"
install -m 0755 openpersona-cloud /usr/local/bin/openpersona-cloud
id openpersona >/dev/null 2>&1 || useradd --system --home /var/cache/openpersona --shell /usr/sbin/nologin openpersona
install -d -m 0750 -o root -g openpersona /etc/openpersona
[ -f /etc/openpersona/openpersona.env ] || install -m 0640 -o root -g openpersona openpersona.env.example /etc/openpersona/openpersona.env
install -m 0644 openpersona.service /etc/systemd/system/openpersona.service
systemctl daemon-reload
systemctl enable --now openpersona
echo "Edit /etc/openpersona/openpersona.env, then: systemctl restart openpersona"
echo "TLS: install Caddy, copy Caddyfile to /etc/caddy/Caddyfile with your hostname, systemctl reload caddy"
