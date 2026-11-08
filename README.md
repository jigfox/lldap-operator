# lldap-operator

A Kubernetes operator that manages [lldap](https://github.com/lldap/lldap)
users, groups and group memberships from custom resources, so they can live in
Git next to the rest of your cluster configuration.

- `LLDAPGroup` and `LLDAPUser` are namespaced resources in the API group
  `lldap.jigfox.github.io/v1alpha1`.
- Existing users and groups are **adopted**, not recreated.
- Memberships are **owned by the user resource** and only touch groups that an
  `LLDAPGroup` manages. Everything without a resource (built-in groups, `admin`,
  the operator's own account) is left alone.
- No passwords are managed: users set their own (for example through your
  identity provider's reset flow).
- A `--dry-run` mode shows what the operator would change without sending
  anything to lldap.

It talks to lldap over HTTP only (GraphQL on port 17170), no LDAP connection
is needed. Tested against lldap v0.6.2 and v0.6.3.

## Install

```sh
kubectl apply -f https://github.com/jigfox/lldap-operator/releases/download/vX.Y.Z/install.yaml
```

This creates the namespace `lldap-operator-system`, the CRDs, RBAC and the
Deployment. The image is `ghcr.io/jigfox/lldap-operator` (`linux/amd64` and
`linux/arm64`). The operator needs only its own resources, events and
leader-election leases in Kubernetes.

### Credentials

The Deployment reads its lldap connection from the Secret
`lldap-operator-credentials` in its namespace (`envFrom`); the pod does not
start until it exists:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: lldap-operator-credentials
  namespace: lldap-operator-system
stringData:
  LLDAP_URL: http://lldap.lldap.svc:17170   # base URL of the lldap web port
  LLDAP_USERNAME: lldap-operator
  LLDAP_PASSWORD: change-me
```

Create a dedicated lldap user for it (the operator cannot bootstrap its own
account) and put it in the `lldap_admin` group; lldap has no narrower role that
may create users and groups. See [Security notes](#security-notes).

## Examples

```yaml
apiVersion: lldap.jigfox.github.io/v1alpha1
kind: LLDAPGroup
metadata:
  name: family          # lldap group name, unless spec.displayName is set
---
apiVersion: lldap.jigfox.github.io/v1alpha1
kind: LLDAPUser
metadata:
  name: alice           # lldap user id, unless spec.username is set
spec:
  email: alice@example.com
  displayName: Alice Example
  firstName: Alice
  lastName: Example
  groups: [family]      # display names of groups
```

```console
$ kubectl get lldapgroups,lldapusers
NAME                                     DISPLAYNAME   GROUP ID   READY   ADOPTED   AGE
lldapgroup.lldap.jigfox.github.io/family family        7          True    false     1m

NAME                                  USERNAME   READY   ADOPTED   AGE
lldapuser.lldap.jigfox.github.io/alice alice     True    false     1m
```

### LLDAPGroup

| Field | Meaning |
| --- | --- |
| `spec.displayName` | Group name in lldap. Defaults to `metadata.name`. Names starting with `lldap_` (lldap's built-in groups) are rejected. Changing it renames the lldap group, found by its stored `status.groupID`. |
| `spec.deletionPolicy` | `Delete` (default) or `Orphan`. |
| `status` | `groupID`, `displayName`, `adopted`, `observedGeneration`, conditions. |

### LLDAPUser

| Field | Meaning |
| --- | --- |
| `spec.username` | lldap user id. Defaults to `metadata.name`; immutable. |
| `spec.email` | Required. |
| `spec.displayName`, `firstName`, `lastName` | Optional. Empty means "leave whatever lldap has" (so adopting never blanks a field; clearing a field must be done in lldap). |
| `spec.groups` | Display names of the groups the user belongs to. |
| `spec.deletionPolicy` | `Delete` (default) or `Orphan`. |
| `status` | `userID`, `adopted`, `groups` (managed groups the user is in), `observedGeneration`, conditions. |

The defaults from `metadata.name` are applied by the operator, not by the API
server, so `spec` in the cluster stays as you wrote it; `status.userID` and
`status.displayName` show the effective values.

## Semantics

**Adoption.** On first reconcile the operator looks the object up (users by
username, groups by display name, case-insensitively as lldap does). If it
exists it is taken over and `status.adopted` is set to `true`; its fields are
then converged to the spec. Only if it is missing it is created. Adoption never
changes memberships in groups that have no `LLDAPGroup`.

**Memberships.** The user resource decides the memberships in *managed* groups,
that is groups for which an `LLDAPGroup` exists (in any namespace):

- a managed group listed in `spec.groups` is added;
- a managed group the user is in but that is not listed is removed;
- a name in `spec.groups` without an `LLDAPGroup` is never touched. The user is
  still reconciled, but `Ready` is `False` with reason `UnknownGroup` and the
  names in the message. If the `LLDAPGroup` exists but has not been created in
  lldap yet, the reason is `GroupPending` and the user is retried shortly; users
  are also re-queued whenever an `LLDAPGroup` changes;
- memberships in groups without an `LLDAPGroup` (including `lldap_admin`) are
  never added or removed.

**Deletion.** Both kinds use a finalizer. With `deletionPolicy: Delete` the
lldap object is deleted (also if it was adopted), with `Orphan` it stays and
only the finalizer is removed. A group is looked up by its stored id and is
never deleted if it is a built-in `lldap_*` group. A user resource that never
reached lldap (no `status.userID`) deletes nothing.

**Refused.** A user resource whose username is the operator's own lldap
account, or whose `status.userID` differs from the requested username, is
reported as `Ready=False`, reason `Invalid`, and not touched.

**Drift.** Every resource is re-checked every `--sync-period` (default 10m), so
changes made in the lldap UI are corrected. Two resources pointing at the same
lldap object are not detected; avoid it.

**Conditions.** `Ready` (`True` when the lldap object matches the spec) with
reasons `Reconciled`, `UnknownGroup`, `GroupPending`, `LLDAPError`, `Invalid`,
`DryRunPending`; `status.observedGeneration` follows `metadata.generation`.

## Dry run

Start the manager with `--dry-run` to compute every change, log it and emit an
Event (`DryRun: would create user "alice"`, `would add member ...`) without
sending a single mutation to lldap. Status is still updated: a `DryRun`
condition is `True`, and a resource that would change gets `Ready=False`,
reason `DryRunPending`; one that needs no change is `Ready=True`, so a
rollout over an existing lldap can prove that adoption is a no-op. Finalizers
are still managed; deleting a resource in dry-run mode removes nothing from lldap.

```sh
kubectl -n lldap-operator-system patch deployment lldap-operator-controller-manager \
  --type json -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dry-run"}]'
kubectl get events -A --field-selector reason=DryRun
```

## Flags and environment

| | |
| --- | --- |
| `LLDAP_URL`, `LLDAP_USERNAME`, `LLDAP_PASSWORD` | Required environment. |
| `--dry-run` | See above. |
| `--sync-period` | Drift check interval, default `10m`. |
| `--leader-elect`, `--metrics-bind-address`, `--health-probe-bind-address`, ... | Standard kubebuilder flags. |

## Security notes

- The lldap account needs `lldap_admin`, which is full control of lldap
  (including other admins' passwords). Whoever can create `LLDAPUser`/
  `LLDAPGroup` resources, or read the credentials Secret, can therefore
  manage your directory. Restrict RBAC on these kinds and on the Secret, and
  consider a network policy that only lets the operator reach lldap's web port.
- The operator never adds users to, or removes them from, groups without an
  `LLDAPGroup`, but it can create a user and put it in any group that has one.
  Do not create an `LLDAPGroup` for a group that grants privileges unless you
  mean to delegate that right to everyone allowed to write `LLDAPUser`s.
- It logs in with a JWT from `/auth/simple/login` and logs in again when the
  token is about to expire or lldap answers 401. Use TLS to lldap if the
  traffic leaves the node.
- The container runs as non-root with a read-only root filesystem and no
  capabilities. Requests: 10m CPU / 64Mi, memory limit 128Mi.

## Development

Everything runs through [mise](https://mise.jdx.dev): `mise.toml` pins Go,
golangci-lint, kubebuilder, controller-gen, kustomize and setup-envtest, and
defines the tasks (there is no Makefile). `mise install` once, then:

```sh
mise run test               # generate, manifests, fmt, vet, then unit tests + envtest
mise run test-integration   # against a real lldap container, needs Docker
mise run lint               # golangci-lint with the logcheck plugin; lint:fix to autofix
mise run manifests          # CRDs and RBAC, after changing api/ or RBAC markers
mise run generate           # DeepCopy methods
mise run build              # bin/manager
IMG=ghcr.io/jigfox/lldap-operator:dev mise run build-installer   # dist/install.yaml
mise tasks                  # all tasks (also docker:build, docker:buildx, install, deploy, ...)
```

`kubebuilder` scaffolding commands (`create api`, ...) still work, but any
Makefile target they would add has to be mirrored as a mise task by hand.

The integration tests default to `lldap/lldap:v0.6.2`; set `LLDAP_IMAGE` to try
another tag. The GraphQL queries follow
[`schema.graphql`](https://github.com/lldap/lldap/blob/v0.6.3/schema.graphql)
of lldap v0.6.3.

Layout: `api/v1alpha1` (types), `internal/lldap` (client behind an interface,
`fake` for tests), `internal/controller` (reconcilers), `config/` (kustomize).

Releases: push a tag `vX.Y.Z`; the workflow builds and pushes the multi-arch
image and attaches `install.yaml` to the GitHub release.

## License

MIT, see [LICENSE](LICENSE).
