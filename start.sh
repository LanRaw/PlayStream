#!/bin/sh
set -eu
cd "$(dirname "$0")"

if [ -z "${RELAY_HOST_IP:-}" ]; then
    case "$(uname -s)" in
        Darwin)
            interface=$(route -n get default 2>/dev/null | awk '$1 == "interface:" { print $2; exit }')
            if [ -n "$interface" ]; then
                RELAY_HOST_IP=$(ipconfig getifaddr "$interface" 2>/dev/null || true)
            fi
            if [ -z "${RELAY_HOST_IP:-}" ]; then
                for interface in $(networksetup -listallhardwareports 2>/dev/null | awk '$1 == "Device:" && $2 ~ /^en[0-9]+$/ { print $2 }'); do
                    RELAY_HOST_IP=$(ipconfig getifaddr "$interface" 2>/dev/null || true)
                    [ -z "$RELAY_HOST_IP" ] || break
                done
            fi
            ;;
        Linux)
            RELAY_HOST_IP=$(ip -4 route get 192.0.2.1 2>/dev/null |
                awk '{ for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit } }')
            ;;
    esac
    if [ -n "${RELAY_HOST_IP:-}" ]; then
        export RELAY_HOST_IP
    fi
fi

if [ "$#" -eq 0 ]; then
    set -- --build
fi
docker compose up -d "$@"
binding=$(docker compose port playstream 8080)
case "${binding%:*}" in
    0.0.0.0|127.0.0.1) printf '本机管理页 http://127.0.0.1:%s\n' "${binding##*:}" ;;
esac
if [ -n "${RELAY_HOST_IP:-}" ]; then
    printf '局域网管理页 http://%s:%s\n' "$RELAY_HOST_IP" "${binding##*:}"
else
    printf '通过电脑的局域网 IP 打开管理页，接收器会自动识别 PS5 首选 DNS 地址。\n'
fi
