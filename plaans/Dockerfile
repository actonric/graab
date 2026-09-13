# plaans: a static site served by nginx.
#
# Build and run locally:
#   docker build -t plaans .
#   docker run --rm -p 8080:8080 plaans
FROM nginx:1.27-alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY public/ /usr/share/nginx/html/

EXPOSE 8080
