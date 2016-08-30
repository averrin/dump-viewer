FROM ubuntu
RUN echo "Europe/Moscow" > /etc/timezone && dpkg-reconfigure -f noninteractive tzdata
EXPOSE 9900
COPY . /app/
WORKDIR /app
