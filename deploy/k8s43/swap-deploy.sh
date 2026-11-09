#!/bin/zsh
# 跳板部署脚本(v5.8.1 起,deploy-4357.sh 退休):VPN 断时经 215 跳板+GitLab 443 中转+nc 部署 43。
# 用法: VERSION=v5.8.1 ./deploy/k8s43/swap-deploy.sh
# 前置: bin/argus(arm64,已 build);215 ssh 别名(root);43 root 密码(sshpass)。
# 血泪教训(全中过):①curl 从 GitLab 拉的文件无 x 位——Dockerfile 必须 COPY --chmod=0755,
#   否则镜像内 644 → uid1000 exec permission denied → CrashLoop(生产 3h+ 停机实录 v5.8.1);
# ②ctr 同 tag 重导 kubelet 不重解析("already present")——必须全新 tag(set image 前先
#   ctr run 探针验证镜像内文件 mode);③上传/拉取后必须校验大小+ELF magic(首次事故 27 字节
#   404 页面被 COPY 进镜像);④后台自动部署与人工部署必须互斥(时序竞争曾把坏镜像推上生产)。
set -e
V=${VERSION:?需 VERSION=vx.y.z}
[ -f bin/argus ] || { echo "缺 bin/argus"; exit 1; }
T=$(ssh 215 'grep "^ARGUS_GITLAB_TOKEN=" /opt/argus/argus.env | cut -d= -f2')
curl -sk --max-time 300 --request PUT --header "PRIVATE-TOKEN: $T" \
  --upload-file bin/argus "https://git.example.com/api/v4/projects/1340/packages/generic/argus-bin/$V/argus-linux-arm64" -o /dev/null -w "up=%{http_code}\n"
ssh 215 "T=$T; curl -sk -L --header \"PRIVATE-TOKEN: \$T\" -o /tmp/swapbuild/argus 'https://git.example.com/api/v4/projects/1340/packages/generic/argus-bin/$V/argus-linux-arm64' \
  && chmod 0755 /tmp/swapbuild/argus \
  && [ \$(stat -c %s /tmp/swapbuild/argus) -gt 10000000 ] \
  && [ \"\$(head -c 4 /tmp/swapbuild/argus | od -An -c | tr -d ' \n')\" = '177ELF' ] \
  && cd /tmp/swapbuild && DOCKER_BUILDKIT=1 docker build --platform linux/arm64 -f /tmp/swap.Dockerfile -t argus:\$V . >/dev/null 2>&1 \
  && docker save argus:\$V -o /tmp/argus-deploy.tar \
  && (nohup nc -l -p 9998 < /tmp/argus-deploy.tar >/tmp/nc-deploy.log 2>&1 &) && sleep 1 && echo STAGED"
sshpass -p 'REDACTED-DEPLOY-PASSWORD' ssh -o StrictHostKeyChecking=no -J 215 root@10.0.0.43 \
  "nc -w 900 10.0.0.215 9998 > /tmp/argus-deploy.tar \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io images import /tmp/argus-deploy.tar >/dev/null 2>&1 \
  && ctr -a /run/k3s/containerd/containerd.sock -n k8s.io run --rm docker.io/library/argus:\$V deployprobe sh -c 'test -x /app/argus' \
  && kubectl -n argus set image deploy/argus argus=argus:\$V \
  && kubectl -n argus delete pod -l app=argus --grace-period=0 --force >/dev/null 2>&1 || true \
  && kubectl -n argus rollout status deploy/argus --timeout=180s >/dev/null \
  && kubectl -n argus logs deploy/argus --since=1m 2>/dev/null | grep http_ready | tail -1 \
  && rm -f /tmp/argus-deploy.tar"
ssh 215 "rm -f /tmp/argus-deploy.tar /tmp/nc-deploy.log /tmp/swapbuild/argus; T=$T; PID=\$(curl -sk --header \"PRIVATE-TOKEN: \$T\" 'https://git.example.com/api/v4/projects/1340/packages?per_page=5' | python3 -c \"import json,sys; print(next((p['id'] for p in json.load(sys.stdin) if p['name']=='argus-bin'),'none'))\"); [ \"\$PID\" != none ] && curl -sk --request DELETE --header \"PRIVATE-TOKEN: \$T\" https://git.example.com/api/v4/projects/1340/packages/\$PID -o /dev/null -w relay_del=%{http_code}"
