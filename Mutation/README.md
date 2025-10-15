# Webhook Server and MutatingConfiguration
- 웹 훅 서버의 예시와 MutatingWebhookConfiguration 리소스를 실습하기 위한 리포지토리입니다.
- This repository provides an example of a webhook server and a corresponding MutatingWebhookConfiguration resource for hands-on practice.
  - Kubernetes Version: 1.29
  - Cluster: Kubeadm

## 사전 준비(Prerequisites)
- Docker가 미리 설치되어 있어야 합니다. 
- 컨테이너를 만들기 위해 go 언어가 설치되어야 합니다.
- 설치할 go 언어: 1.24.4
- Docker must be pre-installed.
- Go language is required to build the container.
- Go version to install: 1.24.4
  ```
  wget https://go.dev/dl/go1.24.4.linux-amd64.tar.gz
  tar -C /usr/local -xzf go1.24.4.linux-amd64.tar.gz
  export PATH=$PATH:/usr/local/go/bin
  ```
- Go 설치 확인
- Check Go installation
  ```
  go version
  ```
## Webhook 서버 컨테이너 만들기(Build Webhook Server Container)
- 본 서비스는 metadata.label에 change:image가 있을 경우 다른 컨테이너 image(ex) jeongseungjun/criu-test:nginx)로 바꿔주는 서비스입니다.
- This webhook service changes the container image (e.g., jeongseungjun/criu-test:nginx) if metadata.labels contains change:image.
1. Git clone
```
git clone https://github.com/GProjectdev/Mutating.git
```
2. 디렉토리 이동(Move to the directory)
```
cd Mutation/webhook-server
```
3. Docker file 시행(로그인은 따로 해줘야 합니다.)(Build and push the Docker image (requires Docker login))
```
buildah bud -t <repository-name>\
  --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 \
  -f Dockerfile .
buildah push <repository-name>
```
## Webhook 서버 배포(Deploy Webhook Server)
1. Webhook 서버를 위한 CA 만들기(openssl 이용)(Generate the CA and TLS certificates (using openssl))
```
# 1. CA 키 생성(Generate CA private key)
openssl genrsa -out ca.key 2048

# 2. CA 인증서 생성 (Generate CA certificate)
openssl req -x509 -new -nodes -key ca.key -subj "/CN=webhook-ca" -days 3650 -out ca.crt

# 3. 서버용 키 생성 (Generate server private key)
openssl genrsa -out tls.key 2048

# 4. 서버 CSR (인증서 서명 요청) 생성(Generate server CSR (Certificate Signing Request))
openssl req -new -key tls.key -subj "/CN=webhook-service.webhook-system.svc" -out server.csr

# 5. 서버 인증서 서명 (CA로)(Sign the server certificate with the CA)
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out tls.crt -days 3650 -extensions v3_ext -extfile <(cat <<EOF
[ v3_ext ]
subjectAltName = @alt_names

[ alt_names ]
DNS.1 = webhook-service
DNS.2 = webhook-service.webhook-system
DNS.3 = webhook-service.webhook-system.svc
EOF
)

# 이 후 이 명령어를 이용해 ca.crt 값을 MutatingWebhookConfiguration yaml파일의 caBundle에 넣어주어야 한다.(Then, insert the base64-encoded output of the CA certificate into the 'caBundle' field in the MutatingWebhookConfiguration YAML.)
cat ca.crt | base64 | tr -d '\n'
```
2. Secret 생성(Create a TLS secret)
 ```
 kubectl create secret tls webhook-server-tls \
--cert=tls.crt \
--key=tls.key \
-n webhook-system
 ```
3. Webhook 서버 배포(순서 중요)(Deploy the webhook server (order matters))
```
cd ../mutating-yaml
kubectl apply -f mutating-resources.yaml
kubectl apply -f muutating-configuration.yaml
```
## Test
```
cd ..
kubectl apply -f test.yaml
kubectl get po <POD_NAME> o -yaml
```
