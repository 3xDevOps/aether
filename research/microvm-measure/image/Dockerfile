# The agents test image (standard + pinned Claude Code and Codex), plus
# what the measurement harness needs: sshd for control, tmux to keep a
# TUI agent alive, fio for disk I/O.
ARG AGENTS=aether1-agents
FROM ${AGENTS}
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-server tmux fio iproute2 procps kmod \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd && ssh-keygen -A \
    && printf 'PermitRootLogin prohibit-password\nUseDNS no\n' >/etc/ssh/sshd_config.d/a1.conf
COPY init.sh /sbin/a1-init
COPY a1.pub /etc/ssh/a1_authorized_keys
RUN chmod 755 /sbin/a1-init \
    && echo 'AuthorizedKeysFile .ssh/authorized_keys /etc/ssh/a1_authorized_keys' >>/etc/ssh/sshd_config.d/a1.conf
