# Dex between the hub and GitHub: GitHub's OAuth apps do not speak OpenID Connect, Dex does. The Dex image renders this template
# (gomplate) from the environment at start.
issuer: https://{{ getenv "EGZO_AUTH_HOST" }}/dex

storage:
  type: sqlite3
  config:
    file: /var/dex/dex.db

web:
  http: 0.0.0.0:5556

oauth2:
  skipApprovalScreen: true   # the hub is the only client: no "Grant access" page

# Only GitHub, so Dex goes straight to it.
connectors:
  - type: github
    id: github
    name: GitHub
    config:
      clientID: "{{ getenv "GITHUB_CLIENT_ID" }}"
      clientSecret: "{{ getenv "GITHUB_CLIENT_SECRET" }}"
      redirectURI: https://{{ getenv "EGZO_AUTH_HOST" }}/dex/callback
{{- if getenv "GITHUB_ORG" }}
      # Members of this organization only. The hub still lets nobody in without an invite.
      orgs:
        - name: "{{ getenv "GITHUB_ORG" }}"
{{- end }}

staticClients:
  - id: egzo-hub
    name: egzo hub
    secretEnv: EGZO_OIDC_CLIENT_SECRET
    redirectURIs:
      - https://{{ getenv "EGZO_HUB_HOST" }}/auth/callback
