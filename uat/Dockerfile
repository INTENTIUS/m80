FROM python:3.12-slim
ARG TARGETARCH
RUN apt-get update -qq && apt-get install -qq -y curl ca-certificates jq unzip less groff >/dev/null && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/v1.31.0/bin/linux/${TARGETARCH}/kubectl" && chmod +x /usr/local/bin/kubectl
RUN curl -fsSL -o /usr/local/bin/microvm \
      "https://github.com/codriverlabs/KubeMicroVM/releases/latest/download/microvm-linux-${TARGETARCH}" \
    && chmod +x /usr/local/bin/microvm
RUN case "${TARGETARCH}" in amd64) A=x86_64 ;; arm64) A=aarch64 ;; esac; \
    curl -fsSL -o /tmp/awscli.zip "https://awscli.amazonaws.com/awscli-exe-linux-${A}.zip" \
    && unzip -q /tmp/awscli.zip -d /tmp && /tmp/aws/install >/dev/null && rm -rf /tmp/awscli.zip /tmp/aws
RUN pip install --no-cache-dir robotframework>=7.0 >/dev/null
WORKDIR /uat
ENTRYPOINT ["robot"]
