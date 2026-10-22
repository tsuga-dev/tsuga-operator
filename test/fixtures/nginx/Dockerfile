FROM nginx:1.27
COPY default.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
# nginx drives no traffic of its own; the loop calls it every two seconds.
# Access logs already go to stdout in the base image.
CMD ["sh", "-c", "(while true; do sleep 2; curl -fsS -o /dev/null http://127.0.0.1:8080/work; done) & exec nginx -g 'daemon off;'"]
