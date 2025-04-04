FROM alpine

WORKDIR /build

COPY proxy .

CMD [". /proxy"]