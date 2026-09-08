# The image TestIntegrationGitHubConnect runs both its environment terminal
# and its run in: git, and the ssh-keygen git signs commits with.
#
# The container user is the test process's own, so the bind-mounted run
# checkout, member home and bare upstream repository have one owner on both
# sides of the mount and git's ownership check never fires. A root test
# process (CI) keeps the image's root user and its /root home.
FROM alpine:3.21

RUN apk add --no-cache git openssh-keygen

ARG UID=0
ARG GID=0
# git reads the passwd entry of the uid it runs as before it will write a
# commit object, so a non-root uid needs one.
RUN [ "$UID" = 0 ] || adduser -D -u "$UID" -h /home/aether agent
USER $UID:$GID
