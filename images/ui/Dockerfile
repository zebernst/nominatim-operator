# nominatim-ui — static Nominatim debug UI from upstream release packages.
# Source: https://github.com/osm-search/nominatim-ui/releases (not mediagis).
# Serves dist/ via unprivileged nginx on port 8080.
ARG NOMINATIM_UI_VERSION=3.12.0

FROM nginxinc/nginx-unprivileged:1.27-alpine

ARG NOMINATIM_UI_VERSION
USER root
RUN apk add --no-cache ca-certificates curl \
	&& curl -fsSL \
		"https://github.com/osm-search/nominatim-ui/releases/download/v${NOMINATIM_UI_VERSION}/nominatim-ui-${NOMINATIM_UI_VERSION}.tar.gz" \
		-o /tmp/nominatim-ui.tar.gz \
	&& mkdir -p /tmp/nominatim-ui \
	&& tar -xzf /tmp/nominatim-ui.tar.gz -C /tmp/nominatim-ui --strip-components=1 \
	&& rm -rf /usr/share/nginx/html/* \
	&& cp -a /tmp/nominatim-ui/dist/. /usr/share/nginx/html/ \
	&& rm -rf /tmp/nominatim-ui /tmp/nominatim-ui.tar.gz \
	&& apk del curl \
	&& chown -R nginx:nginx /usr/share/nginx/html

COPY images/ui/entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh

USER nginx
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
