FROM ubuntu:20.04

RUN apt-get update

RUN apt-get install -y wget software-properties-common gnupg2 winbind xvfb

RUN dpkg --add-architecture i386
RUN wget -nc https://dl.winehq.org/wine-builds/winehq.key
RUN apt-key add winehq.key
RUN add-apt-repository 'deb https://dl.winehq.org/wine-builds/ubuntu/ focal main'
RUN apt-get update
RUN apt-get install -y winehq-stable

RUN apt-get install -y winetricks

RUN apt-get clean -y
RUN apt-get autoremove -y

ENV WINEDEBUG=fixme-all

RUN winetricks msxml6

COPY game/feeding_frenzy /root/feeding_frenzy
COPY startup.sh /root/startup.sh
RUN chmod gou+x /root/startup.sh

EXPOSE 9000

CMD ["/root/startup.sh"]