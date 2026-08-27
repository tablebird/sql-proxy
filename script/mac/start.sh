#!/bin/bash
# sql proxy
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" &>/dev/null && pwd)
"${SCRIPT_DIR}/sql-proxy" -config config.yaml
read -p "按回车键退出..."