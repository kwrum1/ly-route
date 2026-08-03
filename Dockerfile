FROM ly-flow/frontend:base-local

ARG PRODUCT=gateway

RUN rm -f /etc/nginx/templates/*.template

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY frontend/${PRODUCT}/ /usr/share/nginx/html/

HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1

EXPOSE 80
