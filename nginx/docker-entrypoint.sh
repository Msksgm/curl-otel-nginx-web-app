#!/bin/sh
set -e

# 環境変数を nginx.conf.template から nginx.conf に展開
envsubst '${NEW_RELIC_API_KEY}' < /etc/nginx/nginx.conf.template > /etc/nginx/nginx.conf

# Nginx を起動
exec nginx -g 'daemon off;'