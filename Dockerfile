FROM ubuntu:22.04

ARG USERNAME=semaphore
ARG USER_UID=1000
ARG USER_GID=$USER_UID

RUN apt-get update -y && apt-get install -y --no-install-recommends ca-certificates curl
RUN update-ca-certificates

# Create the user
RUN groupadd --gid $USER_GID $USERNAME && \
  useradd --uid $USER_UID --gid $USER_GID -m $USERNAME

COPY build/controller /

USER $USERNAME
WORKDIR /home/semaphore
HEALTHCHECK NONE

ENTRYPOINT ["/controller"]
