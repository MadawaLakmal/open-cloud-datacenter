# Architecture

Diagrams of what this operator is, where it runs, and what happens when a
user creates a registry. Read in order — each diagram builds on the previous.

---

## 1. One Kubernetes cluster on Harvester

Everything runs in one Kubernetes cluster sitting on Harvester nodes.
dc-api (the WSO2 control plane), the registry operator, and every tenant's
Harbor are all pods inside that single cluster.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Harvester nodes (VMs / bare-metal)              │
│                                                                         │
│   ┌─────────────────────────────────────────────────────────────────┐   │
│   │              Kubernetes cluster (one)                           │   │
│   │                                                                 │   │
│   │   pods, services, secrets, PVCs, ... all live here              │   │
│   │   storage backed by Longhorn                                    │   │
│   │                                                                 │   │
│   └─────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Three layers of namespaces

The cluster is sliced into three kinds of namespaces, each with a clear job.

```
┌────────────────────────────────────────────────────────────────────────────┐
│  Kubernetes cluster                                                        │
│                                                                            │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ registry-system            (platform tier — operator lives here)     │  │
│  │   ┌─────────────────────┐         ┌──────────────────────────┐       │  │
│  │   │  dc-api pod(s)      │         │  registry-operator pod   │       │  │
│  │   │  (external project) │         │  (this repo)             │       │  │
│  │   └─────────────────────┘         └──────────────────────────┘       │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ dc-tenant-acme           (tenant tier — one per tenant)              │  │
│  │   ┌──────────────────────────────┐  ┌─────────────────────────────┐  │  │
│  │   │ Harbor pods                  │  │  RegistryBackend CR         │  │  │
│  │   │  core / registry / portal /  │  │  "harbor"                   │  │  │
│  │   │  jobservice / postgres /     │  └─────────────────────────────┘  │  │
│  │   │  redis / trivy (opt-in)      │  ┌─────────────────────────────┐  │  │
│  │   └──────────────────────────────┘  │  Longhorn PVCs              │  │  │
│  │                                     └─────────────────────────────┘  │  │
│  │                                     ┌─────────────────────────────┐  │  │
│  │                                     │  harbor-admin Secret        │  │  │
│  │                                     │  (username + admin pwd)     │  │  │
│  │                                     └─────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ dc-acme-billing          (project tier — one per dc-api project)     │  │
│  │   ┌─────────────────────┐    ┌─────────────────────────────────┐     │  │
│  │   │ RegistryInstance CR │    │  registry-<uuid>-creds Secret   │     │  │
│  │   │  "billing-registry" │    │  username: robot$xyz            │     │  │
│  │   └─────────────────────┘    │  password: <one-time>           │     │  │
│  │                              │  harbor_url: harbor-...svc...   │     │  │
│  │                              └─────────────────────────────────┘     │  │
│  │                                                                      │  │
│  │  (No Harbor pods here. The Harbor-project lives inside the           │  │
│  │   tenant's Harbor — it's an entry in Harbor's database, not          │  │
│  │   a separate deployment.)                                            │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────────┘
```

| Tier | Namespace pattern | What's in it |
|---|---|---|
| Platform | `registry-system` | The operator's manager Deployment. dc-api lives in its own platform-tier namespace. |
| Tenant | `dc-tenant-<slug>` | Harbor cluster for that tenant + admin Secret + Longhorn PVCs. |
| Project | `dc-<tenant>-<project>` | RegistryInstance + robot-credentials Secret. No Harbor pods. |

---

## 3. Who creates what

```
   end user
      │
      │ HTTPS
      ▼
┌──────────────┐
│   dc-api     │  ── creates ──▶  RegistryBackend CR  (in dc-tenant-acme)
│  (external)  │  ── creates ──▶  RegistryInstance CR (in dc-acme-billing)
└──────────────┘
      ▲
      │ watches CRs via the Kubernetes API
      │
┌──────────────────────┐
│  registry-operator   │  ── helm install (vendored chart) ──▶  Harbor pods + PVCs + Services
│   (this repo)        │  ── calls Harbor API              ──▶  Harbor-projects + robot accounts
│                      │  ── writes Secret                 ──▶  registry-<uuid>-creds
└──────────────────────┘
      ▲
      │ HTTPS (Harbor admin API)
      ▼
┌──────────────────────┐
│   Harbor pods        │  ── stores image layers ──▶  Longhorn PVCs
│   (in tenant ns)     │
└──────────────────────┘
```

dc-api never talks to Harbor directly. Operator never talks to end users
directly. Each layer has one job.

---

## 4. First project in a tenant gets a registry

```
   user                dc-api                operator              Harbor          Longhorn
    │                    │                      │                    │                │
 1. │── "give me a       │                      │                    │                │
    │   registry for     │                      │                    │                │
    │   acme/billing" ──▶│                      │                    │                │
    │                    │                      │                    │                │
 2. │                    │── create namespace   │                    │                │
    │                    │   dc-tenant-acme     │                    │                │
    │                    │── create RegistryBackend                  │                │
    │                    │   "harbor"           │                    │                │
    │                    │                      │                    │                │
 3. │                    │                      │── notices new      │                │
    │                    │                      │   Backend          │                │
    │                    │                      │── generates admin  │                │
    │                    │                      │   password Secret  │                │
    │                    │                      │── helm install     │                │
    │                    │                      │   (vendored chart) ▶                │
    │                    │                      │                    │── PVCs ───────▶│
    │                    │                      │                    │── pods coming  │
    │                    │                      │                    │   up …         │
    │                    │                      │                    │   (3–5 min)    │
    │                    │                      │◀──── pods Ready ───│                │
    │                    │                      │── set status.phase │                │
    │                    │                      │   = Ready          │                │
    │                    │◀── Backend Ready ────│                    │                │
    │                    │                      │                    │                │
 4. │                    │── create namespace   │                    │                │
    │                    │   dc-acme-billing    │                    │                │
    │                    │── create RegistryInstance                 │                │
    │                    │   backendRef={harbor, dc-tenant-acme}     │                │
    │                    │                      │                    │                │
 5. │                    │                      │── notices new      │                │
    │                    │                      │   Instance         │                │
    │                    │                      │── reads admin pwd  │                │
    │                    │                      │── HTTP POST ──────▶│                │
    │                    │                      │   /api/v2.0/projects                │
    │                    │                      │── HTTP POST ──────▶│                │
    │                    │                      │   /api/v2.0/projects/billing/robots │
    │                    │                      │   ◀── username + secret ────────────│
    │                    │                      │── writes creds Secret               │
    │                    │                      │── set status.phase = Ready          │
    │                    │◀── Instance Ready ───│                    │                │
    │◀── credentials ────│                      │                    │                │
    │                    │                      │                    │                │
 6. │── docker login + push to harbor-harbor-core.dc-tenant-acme.svc.cluster.local
    │                                                                ▶── image saved
```

---

## 5. Second project in the same tenant

Harbor is already up. dc-api creates only a new `RegistryInstance`. The
operator just adds another Harbor-project + robot inside the same Harbor.

```
   user                dc-api                operator                Harbor
    │                    │                      │                      │
 1. │── "give me a       │                      │                      │
    │   registry for     │                      │                      │
    │   acme/payments"──▶│                      │                      │
    │                    │                      │                      │
 2. │                    │── create RegistryInstance                   │
    │                    │   backendRef={harbor, dc-tenant-acme}       │
    │                    │   (same backend as billing)                 │
    │                    │                      │                      │
 3. │                    │                      │── HTTP POST ────────▶│
    │                    │                      │   /api/v2.0/projects │
    │                    │                      │   ("payments")       │
    │                    │                      │── HTTP POST ────────▶│
    │                    │                      │   /.../robots        │
    │                    │                      │── writes creds Secret│
    │                    │◀── Instance Ready ───│                      │
    │◀── credentials ────│                      │                      │
```

---

## 6. Delete flows

```
   dc-api          operator              Harbor
    │                │                      │
 1. │── DELETE       │                      │
    │   RegistryInstance billing            │
    │                │                      │
 2. │                │── HTTP DELETE ──────▶│ /api/v2.0/projects/<id>/robots/<id>
    │                │── HTTP DELETE ──────▶│ /api/v2.0/projects/<id>
    │                │── removes instance-cleanup finalizer
    │                │   CR is garbage-collected
```

```
   dc-api          operator              Harbor
    │                │                      │
 1. │── DELETE       │                      │
    │   RegistryBackend harbor              │
    │                │                      │
 2. │                │── helm uninstall ───▶│ Harbor pods/PVCs/Services removed
    │                │── removes backend-cleanup finalizer
```

---

## 7. What runs where (reference)

| Thing | Lives in | Created by | Owns |
|---|---|---|---|
| `dc-api` pod | `dc-api`'s own platform namespace | external project | — |
| `registry-operator` manager pod | `registry-system` | `make deploy` (us) | the two CRD types |
| `RegistryBackend` CR | `dc-tenant-<slug>` | dc-api | the tenant's Harbor |
| Harbor pods + PVCs | `dc-tenant-<slug>` | operator (via vendored Helm chart) | image layers |
| `harbor-admin-credentials` Secret | `dc-tenant-<slug>` | operator | — |
| `RegistryInstance` CR | `dc-<tenant>-<project>` | dc-api | one Harbor-project |
| `registry-<uuid>-creds` Secret | `dc-<tenant>-<project>` | operator | robot credentials |
| Harbor-project | inside Harbor's DB | operator (via API) | repos + robots |
