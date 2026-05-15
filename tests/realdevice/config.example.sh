# tests/realdevice/config.example.sh
# 复制为 config.sh 后按实机核对。config.sh 已被 .gitignore 忽略 —— 切勿提交凭证。
ROUTER_SSH="192.168.50.1"              # 路由器 ssh 目标（host 或 ssh_config 别名）
LAN_CLIENT_SSH="192.168.50.12"         # 必填：连通性探针从这台 LAN 客户端发起
SINGROUTER="sing-router"               # 路由器上的 sing-router 二进制（PATH 名或绝对路径）
INITD="/opt/etc/init.d/S99sing-router" # Entware init.d 脚本（恢复 daemon 用）
RUNDIR="/opt/home/sing-router"         # daemon 运行根目录
CONFIG_DIR="config.d"                  # 配置目录，相对 RUNDIR
ROUTE_TABLE="7892"                     # sing-router 使用的策略路由表 id
ROUTE_MARK="0x7892"                    # sing-router 使用的 fwmark（ip rule fwmark ... lookup ROUTE_TABLE）
