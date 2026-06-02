# Expose the dashboard externally with auth

This guide shows you how to expose the agent's dashboard sidecar through an Ingress with TLS and an external auth proxy. Assumes you have already enabled the dashboard; see [Enable the dashboard sidecar](enable-dashboard.md) first.

!!! warning "Read this before exposing the dashboard publicly"
    The dashboard's SPA uses an ephemeral session token that upstream embeds in the rendered HTML. There is no way to override or rotate this token from outside the dashboard process. **`/api/status` is unauthenticated by upstream design** and remains so regardless of what you do at the edge. Treat the dashboard as "exposes everything to anyone who can reach `/`" and put auth in front of it.

## What you'll set up

1. An Ingress with TLS for the agent's dashboard Service.
2. An external auth check via oauth2-proxy or nginx `auth-url`.
3. (Optional) cert-manager wiring for the TLS cert.

The operator does not need to know about any of this. `spec.dashboard.ingress.annotations` is a passthrough field: set the auth annotations there and the operator copies them onto the generated Ingress.

## Pattern 1: nginx + oauth2-proxy

Assumes you have oauth2-proxy running at `https://auth.example.com`, configured to authenticate against your IdP (Google, GitHub, OIDC, etc.) and returning 202 on valid sessions.

```yaml
apiVersion: hermes.k8s.undermountain.cc/v1alpha1
kind: HermesAgent
metadata: { name: my-agent, namespace: hermes }
spec:
  image: docker.io/nousresearch/hermes-agent:v2026.4.30
  storage:
    persistentVolumeClaim:
      accessModes: [ReadWriteOnce]
      resources: { requests: { storage: 10Gi } }
  llmDefaultProvider: anthropic
  llmProviders:
    - name: anthropic
      env:
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef: { name: my-agent-creds, key: ANTHROPIC_API_KEY }
  dashboard:
    enabled: true
    ingress:
      enabled: true
      ingressClassName: nginx
      host: hermes.example.com
      tls:
        secretName: hermes-tls
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod
        nginx.ingress.kubernetes.io/auth-url: "https://auth.example.com/oauth2/auth"
        nginx.ingress.kubernetes.io/auth-signin: "https://auth.example.com/oauth2/start?rd=$scheme://$host$request_uri"
```

What happens at request time:

1. User hits `https://hermes.example.com/` → nginx checks `auth-url`.
2. If `auth-url` returns 202, nginx proxies to the dashboard sidecar.
3. If `auth-url` returns 401, nginx redirects to `auth-signin` (oauth2-proxy's login flow).
4. After IdP login, oauth2-proxy sets a session cookie scoped to `example.com`; the user lands back on `hermes.example.com/`.

## Pattern 2: traefik forwardAuth

For traefik users, the equivalent uses a `Middleware` resource.

Create the middleware once per namespace:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: oauth2-auth
  namespace: hermes
spec:
  forwardAuth:
    address: https://auth.example.com/oauth2/auth
    trustForwardHeader: true
    authResponseHeaders:
      - X-Auth-Request-Email
      - X-Auth-Request-User
```

Then reference it from the HermesAgent:

```yaml
spec:
  dashboard:
    enabled: true
    ingress:
      enabled: true
      ingressClassName: traefik
      host: hermes.example.com
      tls:
        secretName: hermes-tls
      annotations:
        traefik.ingress.kubernetes.io/router.middlewares: hermes-oauth2-auth@kubernetescrd
        cert-manager.io/cluster-issuer: letsencrypt-prod
```

## Provisioning TLS with cert-manager

If you have cert-manager installed for issuing TLS certs (a different use case from the operator's deleted admission-webhook dependency), the `cert-manager.io/cluster-issuer` annotation above is all you need. cert-manager picks up the Ingress, mints a Certificate, and populates the `hermes-tls` Secret. Existing cert-manager Ingress walkthroughs apply unchanged.

## Verify

After applying, watch the Ingress get an address:

```bash
kubectl -n hermes get ingress hermes-my-agent-dashboard -w
```

Then browse to `https://hermes.example.com/`. You should be redirected to your IdP, log in, and land on the dashboard SPA.

Confirm the auth-url is being hit:

```bash
# nginx-ingress
kubectl -n ingress-nginx logs deploy/ingress-nginx-controller --tail=50 | grep hermes.example.com
```

Look for `200`/`202` responses to `auth-url` and `200` to `/`. A `401` loop usually means oauth2-proxy is misconfigured for the host or the cookie scope is wrong.

## Caveat: `/api/status` is always unauthenticated

Upstream's `/api/status` endpoint does not consult the session token. With an `auth-url` annotation, nginx gates it at the edge like every other path, but anyone with a valid session has read access to `gateway_running` and per-platform connection state. This is usually fine (it is the same shape the operator reads) but worth knowing.

## See also

- [Enable the dashboard sidecar](enable-dashboard.md) — turning on the dashboard in the first place.
- [Known Hermes upstream behaviours](../concepts/upstream-behaviours.md) — the auth model rationale.
- [Reference: HermesAgent API](../reference/api-reference.md#hermesagentdashboardingress) — every Ingress field documented.
