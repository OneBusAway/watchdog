# Investigation API: scenario review

**Status:** Confirmed 2026-11-02. All nine decisions in Section 7 were accepted and applied to the [API design](investigation-api.md) (D1, D26–D30) and the [schema document](historical-investigation-schema.md#views-for-the-investigation-api). N1–N7 became UC-15 to UC-21. This document records the reasoning behind those changes.
**Reviews:** [Investigation REST API](investigation-api.md), against the [historical investigation schema](historical-investigation-schema.md) and migration `00001`.

The investigation API is meant to let an observer reconstruct an incident in a few requests, without the API being built around any single use case. This review walks every use case in the API design, plus seven incident scenarios the design does not cover yet. It checks each one against a request budget, groups the problems it finds by kind, and proposes general rules that fix them. The last section lists the decisions to confirm before the API design is updated.

## How to read this document

| Section | What you'll find | Read it when |
|---|---|---|
| [1. Budget](#1-budget) | The three tests every scenario must pass | First, it's short |
| [2. Scenarios](#2-scenarios) | The 21 scenarios: the 14 use cases in the design, plus 7 new incidents | First |
| [3. Walkthroughs](#3-walkthroughs) | One table per scenario: the observer's questions, the request that answers each, its cost, and its gap | To see where today's design breaks |
| [4. Gaps by kind](#4-gaps-by-kind) | The five kinds of gap and which scenarios hit each | To see the pattern |
| [5. Proposed rules](#5-proposed-rules) | The seven rules that fix the gaps | The main proposal |
| [6. Rules checked](#6-rules-checked-against-every-scenario) | Every scenario's cost before and after the rules | To check the proposal works |
| [7. Decisions to confirm](#7-decisions-to-confirm) | The nine choices to approve | Last |
| [8. Doc changes](#8-changes-to-the-design-docs-once-confirmed) | Which parts of the design docs change | After confirming |
| [9. Endpoint reference](#9-endpoint-reference) | Every request used here, with a link to its full description | Whenever a request is unfamiliar |

**Links.** Every request, gap ([K1](#k1-related-records-in-the-same-response)–[K5](#k5-data-that-is-not-stored)), rule ([R1](#r1-one-shared-included-section-compound-document)–[R7](#r7-data-that-is-not-stored-stays-out-of-the-api)), budget test ([B1](#b1-about-ten-requests-in-the-typical-case)–[B3](#b3-summary-questions-need-no-paging)) and scenario ID is a link. Requests go to the [endpoint reference](#9-endpoint-reference); `D` numbers go to the decisions in the API design.

**Going back after following a link.** On GitHub or in a browser, press ⌘[ or ⌘← on a Mac, or Alt+← on Windows and Linux. In the VS Code editor, Go Back is Ctrl+- on a Mac and Alt+← on Windows and Linux.

**Cost** in the walkthrough tables is the number of HTTP requests the question takes:

| Cost | Meaning |
|---|---|
| `1`, `2`, … | That many requests |
| `1–3` | One request per distinct target, usually one to three. For example, 600 episodes opened by two polls need two requests, one per poll |
| `pages` | The pages needed to read a whole list. Lists return at most 200 items per page, so 600 episodes take 3 pages |
| `pages + 2` | A whole list plus two more requests |
| `one per identifier` | Grows with the number of stops, trips, vehicles, endpoints or agencies. This fails [B2](#b2-no-question-costs-one-request-per-item) |
| `not possible` | No request answers it today |

---

## 1. Budget

A scenario is within budget when all of these hold:

### B1. About ten requests in the typical case

Resolving a server and agency once ([D10](investigation-api.md#6-implementation-decisions)) is not counted, because clients cache those IDs.

### B2. No question costs one request per item

A question that needs one request per stop, trip, vehicle or agency fails, however small the set is today.

### B3. Summary questions need no paging

Paging is fine when the observer reads every row. It is not fine when the observer only wants a count or a split, such as "how many of these are missing from static GTFS?".

---

## 2. Scenarios

**Use cases already in the design:** [UC-01](#uc-01-unmatched-trips-during-degraded-trip-matching) to [UC-14](#uc-14-incident-timeline) ([Section 2](investigation-api.md#2-investigation-use-cases)).

**New incident scenarios:**

| ID | Scenario | Trigger |
|---|---|---|
| [N1](#n1-unmatched-stop-spike) | Unmatched-stop spike | An agency's unmatched-stop count jumps from about 20 to about 600 |
| [N2](#n2-oba-restart-or-reload) | OBA restart or reload | Many episodes close at the same moment and many reopen minutes later |
| [N3](#n3-flapping-identifiers) | Flapping identifiers | The same stops, trips or vehicles open and close episodes many times a day |
| [N4](#n4-server-wide-incident-across-agencies) | Server-wide incident | Several agencies on one OBA server degrade at the same time |
| [N5](#n5-bounding-box-shrinks-after-a-static-update) | Bounding box shrinks after a static update | A wave of `stopped_out_of_bounds` episodes follows a static GTFS publication |
| [N6](#n6-vehicles-missing-from-an-agencys-view) | Vehicles missing from an agency's view | Anomalies exist on the server but do not appear when filtering by agency |
| [N7](#n7-no-data-or-no-recording) | No data, or no recording? | The API shows nothing for a period, and the observer needs to know whether nothing happened or Watchdog failed to record it |

---

## 3. Walkthroughs

Each table lists the observer's questions in order, the request that answers each one with today's design, and its cost (see [How to read this document](#how-to-read-this-document)). A gap is marked with its kind from [Section 4](#4-gaps-by-kind).

### UC-01: Unmatched trips during degraded trip matching

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which trips were unmatched? | [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list) with `agency_id` and window, `include=static_contexts` | pages | |
| How many are missing from static GTFS, and how many are present but unmatched? | Page through everything and count, or two filtered lists and count | pages | [K2](#k2-counts-and-splits-without-paging) |
| Are they concentrated on one route or block? | Page through everything and group `route_id` on the client | pages | [K2](#k2-counts-and-splits-without-paging) |
| What did OBA's counts say when they opened? | Follow `opening_response` per distinct response (not includable on [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list)) | 1–3 | [K1](#k1-related-records-in-the-same-response) |

### UC-02: Reconstruct unmatched stops after an alert cleared

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which stops, for how long? | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) with window, `include=static_contexts` | pages | |
| What did the opening and closing responses contain? | Follow per distinct response (not includable on [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list)) | 1–5 | [K1](#k1-related-records-in-the-same-response) |

### UC-03: Recurrence of one stop or trip

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Resolve the identifier | [`GET /v1/reported-stops`](#reported-stop-lookup) or [`GET /v1/reported-trips`](#reported-trip-lookup) | 1 | |
| Every retained episode for it | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop) or [`GET /v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes`](#episodes-for-one-trip) | 1 | |

Within budget. The cost problem appears only when the same question is asked for many identifiers ([N1](#n1-unmatched-stop-spike), [N3](#n3-flapping-identifiers)).

### UC-04: Static GTFS status of an unmatched identifier

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Was it in static GTFS during the episode? | [`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`](#static-context-for-a-stop-episode) | 1 | |
| When was each version published? | [`GET /v1/gtfs-static-versions/{gtfs_static_version_id}`](#one-static-gtfs-version) per item ([`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`](#static-context-for-a-stop-episode) has no includes) | 1–3 | [K1](#k1-related-records-in-the-same-response) |
| Did a source's content change? | [`GET /v1/static-gtfs-sources/{source_id}/versions`](#a-static-source-across-versions) | 1 | |

### UC-05: Geographic concentrations of unmatched stops

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Where are unmatched stops concentrated at time `at`? | [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters) | 1 | |
| Which stops are in the top cluster? | [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters) says each item "links to those episodes", but no endpoint can serve that link: [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) has no cluster filter and no "open at" filter | not possible | [K4](#k4-drilling-from-a-summary-to-its-episodes) |

### UC-06: Vehicle with an outdated position or not shown

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Resolve the vehicle | [`GET /v1/vehicles`](#vehicle-lookup) | 1 | |
| Its stale episodes around that time | [`GET /v1/vehicles/{vehicle_entity_id}/anomaly-episodes`](#episodes-for-one-vehicle) with `anomaly_type` and window | 1 | |
| What did the feed report? | [`GET /v1/vehicle-anomaly-episodes/{episode_id}`](#one-vehicle-anomaly-episode) `include=evidence` | 1 | |

Within budget.

### UC-07: Implausible vehicle positions

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which vehicles? | [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) with type and window, `include=opening_evidence` | pages | |
| Why were they invalid, by reason? | Page through everything and count `position_validation_reason` | pages | [K2](#k2-counts-and-splits-without-paging) |
| Which bounding box was applied? | [`GET /v1/bounding-box-versions/{bounding_box_version_id}`](#one-bounding-box-version) per distinct box (two levels away: episode → evidence → box) | 1–2 | |

### UC-08: Feed-wide freshness degradation

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which vehicles were stale? | [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) with `anomaly_type=stale_source_timestamp`, `endpoint_id` | pages | |
| Did they all go stale at the same poll (the feed froze)? | Page through everything and group `opening_response` on the client | pages | [K2](#k2-counts-and-splits-without-paging) |
| Full snapshot or differential? | Follow `opening_response` per distinct response (not includable on [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list)) | 1–3 | [K1](#k1-related-records-in-the-same-response) |
| A few vehicles or the whole fleet? | Fleet size is not stored; only anomalies are | not possible | [K5](#k5-data-that-is-not-stored) |

### UC-09: Effect of a detection rule change

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Rule history | [`GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions`](#rule-history-for-an-anomaly-type) | 1 | |
| Volumes before and after | [`GET /v1/episode-counts`](#episode-counts) twice | 2 | |

Within budget.

### UC-10: Periodic data-quality review

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Totals per type | [`GET /v1/episode-counts`](#episode-counts) | 1 | |
| Which identifiers caused most episodes? | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop) or [`GET /v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes`](#episodes-for-one-trip) per identifier, or page through everything | one per identifier | [K2](#k2-counts-and-splits-without-paging) |

### UC-11: Incidents across a feed URL change

| Question | Request today | Cost | Gap |
|---|---|---|---|
| URLs the server has used | [`GET /v1/servers/{server_id}/vehicle-position-endpoints`](#feed-urls-a-server-has-used) | 1 | |
| Episodes per URL | [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) once per endpoint | one per endpoint | [K2](#k2-counts-and-splits-without-paging) |

### UC-12: How a vehicle anomaly ended

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Close reason and resolution evidence | [`GET /v1/vehicle-anomaly-episodes/{episode_id}`](#one-vehicle-anomaly-episode) `include=evidence` | 1 | |

Within budget.

### UC-13: Effect of a corrective action

| Question | Request today | Cost | Gap |
|---|---|---|---|
| When was the fix published? | [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list) | 1 | |
| Is anything still open? | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) or [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list) with `status=open` | 1 | |
| Did anything open again after the fix? | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) with a window starting at the fix | 1 | |
| Are those repeats of identifiers from before the fix? | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop) per identifier | one per identifier | [K3](#k3-a-per-identifier-fact-on-every-episode) |

### UC-14: Incident timeline

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Episodes in the window | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list), [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list), [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) | 3 + pages | |
| Static publications | [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list) | 1 | |
| Rule changes | [`GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions`](#rule-history-for-an-anomaly-type) per anomaly type | 3 | |
| When did the wave start and peak? | [`GET /v1/episode-counts`](#episode-counts) per sub-window, or page through everything and bucket on the client | many | [K2](#k2-counts-and-splits-without-paging) |

### N1: Unmatched-stop spike

| Question | Request today | Cost | Gap |
|---|---|---|---|
| How big, and is it still going? | [`GET /v1/episode-counts`](#episode-counts) | 1 | |
| Did a static update come just before? | [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list) | 1 | |
| Is it concentrated in one area? | [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters) | 1 | |
| Missing from static GTFS, or present but unmatched? | Page through everything | pages | [K2](#k2-counts-and-splits-without-paging) |
| Did they all open at the same poll? | Page through everything and group on the client | pages | [K2](#k2-counts-and-splits-without-paging) |
| New stops, or chronic offenders? | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop) per stop | 600 | [K3](#k3-a-per-identifier-fact-on-every-episode) |
| Each episode with its opening response | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) with includes; the same response is repeated in every item | pages | [K1](#k1-related-records-in-the-same-response) |

### N2: OBA restart or reload

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Did many episodes close in the same poll? | Page through everything and group `closing_response` | pages | [K2](#k2-counts-and-splits-without-paging) |
| Did the same identifiers reopen within minutes? | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop) per identifier | one per identifier | [K3](#k3-a-per-identifier-fact-on-every-episode) |
| What did OBA's counts say at that poll? | [`GET /v1/oba-metrics-responses/{response_id}`](#one-oba-metrics-response) | 1 | |
| Was OBA failing between those polls? | Not stored: responses are kept only when they open or close an episode | not possible | [K5](#k5-data-that-is-not-stored) |

### N3: Flapping identifiers

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which stops, trips or vehicles had the most episodes this week? | Page through everything and group on the client | pages | [K2](#k2-counts-and-splits-without-paging) |
| How long were the gaps between episodes? | [`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`](#episodes-for-one-stop), [`GET /v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes`](#episodes-for-one-trip) or [`GET /v1/vehicles/{vehicle_entity_id}/anomaly-episodes`](#episodes-for-one-vehicle) per identifier | one per identifier | [K3](#k3-a-per-identifier-fact-on-every-episode) |

### N4: Server-wide incident across agencies

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Which agencies are affected, and how badly? | [`GET /v1/episode-counts`](#episode-counts) once per agency | one per agency | [K2](#k2-counts-and-splits-without-paging) |
| Did they all start at the same server poll? | Page through each agency's list | pages per agency | [K2](#k2-counts-and-splits-without-paging) |

The second question points at OBA itself rather than one agency's data. `onebusaway_metrics_response` is one row per server poll, shared by every agency, so grouping by it answers the question directly.

### N5: Bounding box shrinks after a static update

| Question | Request today | Cost | Gap |
|---|---|---|---|
| How many out-of-bounds episodes, under which box? | Page through [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) with `include=opening_evidence` and group `bounding_box_version` | pages | [K2](#k2-counts-and-splits-without-paging) |
| What were the old and new limits? | [`GET /v1/bounding-box-versions/{bounding_box_version_id}`](#one-bounding-box-version) twice | 2 | |
| Which static version produced the new box? | [`GET /v1/gtfs-static-versions/{gtfs_static_version_id}`](#one-static-gtfs-version) | 1 | |

### N6: Vehicles missing from an agency's view

| Question | Request today | Cost | Gap |
|---|---|---|---|
| How many anomalies on the server have no agency, and why? | Page through [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) with `server_id` and inspect each opening evidence | pages | [K2](#k2-counts-and-splits-without-paging) |
| Show those episodes | No filter for unattributed episodes or for `attribution_reason` | not possible | [K4](#k4-drilling-from-a-summary-to-its-episodes) |

[`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list)'s `agency_id` filter uses the agency in the opening evidence, which is null when attribution failed. Those episodes disappear from every agency-filtered view, and the observer has no sign that anything is missing.

### N7: No data, or no recording?

| Question | Request today | Cost | Gap |
|---|---|---|---|
| Was Watchdog recording during this period? | Not stored. History writes that fail are logged and lost ([D18](investigation-api.md#6-implementation-decisions)), and PostgreSQL outages are deferred in ADR 0001 | not possible | [K5](#k5-data-that-is-not-stored) |

---

## 4. Gaps by kind

### K1. Related records in the same response

- **Appears in:** [UC-01](#uc-01-unmatched-trips-during-degraded-trip-matching), [UC-02](#uc-02-reconstruct-unmatched-stops-after-an-alert-cleared), [UC-04](#uc-04-static-gtfs-status-of-an-unmatched-identifier), [UC-08](#uc-08-feed-wide-freshness-degradation), [N1](#n1-unmatched-stop-spike)
- **Fix:** Sideloading, [R1](#r1-one-shared-included-section-compound-document) and [R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link)

### K2. Counts and splits without paging

- **Appears in:** [UC-01](#uc-01-unmatched-trips-during-degraded-trip-matching), [UC-07](#uc-07-implausible-vehicle-positions), [UC-08](#uc-08-feed-wide-freshness-degradation), [UC-10](#uc-10-periodic-data-quality-review), [UC-11](#uc-11-incidents-across-a-feed-url-change), [UC-14](#uc-14-incident-timeline), [N1](#n1-unmatched-stop-spike)–[N6](#n6-vehicles-missing-from-an-agencys-view)
- **Fix:** Grouped counts backed by views, [R3](#r3-grouped-counts-on-get-v1episode-counts)

### K3. A per-identifier fact on every episode

- **Appears in:** [UC-13](#uc-13-effect-of-a-corrective-action), [N1](#n1-unmatched-stop-spike), [N2](#n2-oba-restart-or-reload), [N3](#n3-flapping-identifiers)
- **Fix:** Derived recurrence fields, [R5](#r5-recurrence-fields-on-every-episode)

### K4. Drilling from a summary to its episodes

- **Appears in:** [UC-05](#uc-05-geographic-concentrations-of-unmatched-stops), [N6](#n6-vehicles-missing-from-an-agencys-view), and every grouped count from [R3](#r3-grouped-counts-on-get-v1episode-counts)
- **Fix:** Matching filters, [R4](#r4-every-dimension-is-also-a-filter)

### K5. Data that is not stored

- **Appears in:** [UC-08](#uc-08-feed-wide-freshness-degradation) fleet size, [N2](#n2-oba-restart-or-reload) polls in between, [N7](#n7-no-data-or-no-recording)
- **Fix:** Prometheus, or a separate change ([R7](#r7-data-that-is-not-stored-stays-out-of-the-api))

[K2](#k2-counts-and-splits-without-paging) is the largest kind by far. Most incident questions are "how is this set split?", and links and includes cannot answer that, however they are designed. Sideloading fixes navigation. Grouped counts fix explanation.

---

## 5. Proposed rules

### R1. One shared `included` section (compound document)

Replaces the per-resource `included` object in [Including related resources](investigation-api.md#including-related-resources).

- A response with `include` has one top-level `included` object. Its keys are link URLs and its values are the representation a `GET` on that URL returns, without that representation's own `included`.
- Each URL appears once, however many items link to it. A page of 600 episodes opened by one poll embeds that response once.
- Resources keep their own fields and `links` unchanged. A client reads `included[item.links.opening_response]`.
- An absent link has no entry. Because the link itself is omitted, a `null` value is no longer needed.
- The server still reads everything in one read-only transaction ([D4](investigation-api.md#6-implementation-decisions)), with one batched query per include name.

```json
{
  "items": [
    { "unmatched_stop_episode_id": 4815, "links": { "self": "/v1/unmatched-stop-episodes/4815", "opening_response": "/v1/oba-metrics-responses/91873" } },
    { "unmatched_stop_episode_id": 4814, "links": { "self": "/v1/unmatched-stop-episodes/4814", "opening_response": "/v1/oba-metrics-responses/91873" } }
  ],
  "included": {
    "/v1/oba-metrics-responses/91873": { "response_id": 91873, "observed_at": "2026-10-23T01:00:12Z" }
  },
  "links": { "self": "/v1/unmatched-stop-episodes?agency_id=3&include=opening_response" }
}
```

Keying by URL rather than by type and ID (as JSON:API does) keeps the existing rule that clients treat URIs as opaque and never build them.

### R2. Which links can be included follows from the kind of link

Replaces the hand-written per-endpoint include lists in Section 4.

- **Every to-one link can be included**, on single resources and on collections. Examples: `reported_stop`, `agency`, `server`, `opening_response`, `closing_response`, `vehicle`, `endpoint`, `rule_version`, `gtfs_static_version`, `bounding_box_version`, `episode`.
- **A to-many link can be included when its target collection is unpaginated.** Those collections are bounded by one parent ([D22](investigation-api.md#6-implementation-decisions)): `static_contexts` ([`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`](#static-context-for-a-stop-episode), [`GET /v1/unmatched-trip-episodes/{episode_id}/static-contexts`](#static-context-for-a-trip-episode)) and `evidence` ([`GET /v1/vehicle-anomaly-episodes/{episode_id}/evidence`](#evidence-for-a-vehicle-episode)). This holds on collections as well.
- `self` and `next` can never be included.
- Includes stay one level deep. A name that is not an includable link of the resource returns `400`, as today.

A new link becomes includable when it is added, so no allow-list needs editing for a new use case.

Two things follow automatically. [`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`](#static-context-for-a-stop-episode) and [`GET /v1/unmatched-trip-episodes/{episode_id}/static-contexts`](#static-context-for-a-trip-episode) items can include `gtfs_static_version`, which fixes [UC-04](#uc-04-static-gtfs-status-of-an-unmatched-identifier). [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list), [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list) and [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) can include `opening_response`, `closing_response` and `endpoint`, which fixes [UC-01](#uc-01-unmatched-trips-during-degraded-trip-matching), [UC-02](#uc-02-reconstruct-unmatched-stops-after-an-alert-cleared) and [UC-08](#uc-08-feed-wide-freshness-degradation).

### R3. Grouped counts on `GET /v1/episode-counts`

`GET /v1/episode-counts` gains two optional parameters:

- `episode_type`: one of `unmatched_stop`, `unmatched_trip`, or a vehicle anomaly code.
- `group_by`: one dimension from the table below. It requires `episode_type` when the dimension applies to only some types.

Each group has the same counts as today (`opened`, `closed` by reason, `open_at_to`), a `value`, and `links`. A group whose value is a resource links to it under the dimension's name (for example `links.agency`), so [R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link) lets the client include it. Every group also has `links.episodes` ([R4](#r4-every-dimension-is-also-a-filter)).

| Dimension | Episode types | Value comes from |
|---|---|---|
| `agency` | all | The episode's agency. For vehicle episodes, the opening evidence's agency, with a `null` group for unattributed vehicles |
| `endpoint` | vehicle | The vehicle's feed URL |
| `opening_response` | all | The poll that opened the episode (for unmatched episodes, the server-wide `response_id`, not the per-agency row) |
| `closing_response` | all | The poll that closed it |
| `static_found` | unmatched | The episode's latest static context |
| `gtfs_static_version` | all | Unmatched: the latest static context. Vehicle: the opening evidence |
| `route_id` | `unmatched_trip`, vehicle | Trip: the latest static context. Vehicle: the opening evidence |
| `cluster` | `unmatched_stop` | The latest static context's cluster assignment under `scheme_version` (default: the newest scheme) |
| `rule_version` | vehicle | The episode's rule version |
| `bounding_box_version` | `stopped_out_of_bounds` | The opening evidence |
| `position_validation_reason` | `invalid_coordinate` | The opening evidence |
| `attribution_reason` | vehicle | The opening evidence |
| `expected_schedule_state` | vehicle | The opening evidence |
| `recurrence` | all | `has_previous_episode` or `no_previous_episode` ([R5](#r5-recurrence-fields-on-every-episode)) |
| `entity` | all | The stop, trip or vehicle |
| `opened_hour`, `opened_day` | all | The UTC hour or day the episode opened |

Limits:
- One dimension per request.
- At most 200 groups, ordered by `opened` descending (time dimensions are ordered by time instead). A response with more groups has `groups_truncated: true`.
- `opened_hour` allows a window of at most 7 days. Every other dimension allows 31 days, as today.

```
GET /v1/episode-counts?agency_id=3&from=2026-10-23T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=static_found
```

```json
{
  "from": "2026-10-23T00:00:00Z",
  "to": "2026-10-24T00:00:00Z",
  "episode_type": "unmatched_stop",
  "group_by": "static_found",
  "items": [
    { "value": false, "opened": 540, "closed": { "not_reported": 12 }, "open_at_to": 528,
      "links": { "episodes": "/v1/unmatched-stop-episodes?agency_id=3&opened_from=2026-10-23T00:00:00Z&opened_to=2026-10-24T00:00:00Z&static_found=false" } },
    { "value": true, "opened": 61, "closed": { "not_reported": 55 }, "open_at_to": 6,
      "links": { "episodes": "/v1/unmatched-stop-episodes?agency_id=3&opened_from=2026-10-23T00:00:00Z&opened_to=2026-10-24T00:00:00Z&static_found=true" } }
  ],
  "groups_truncated": false,
  "links": { "self": "/v1/episode-counts?agency_id=3&from=2026-10-23T00:00:00Z&to=2026-10-24T00:00:00Z&episode_type=unmatched_stop&group_by=static_found" }
}
```

### R4. Every dimension is also a filter

A summary is only useful if the observer can open the episodes behind it. So every `group_by` dimension is also a filter on the matching episode list, and each group's `links.episodes` uses it.

New filters:

| Filter | [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list) | [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list) | [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list) |
|---|---|---|---|
| `opened_from`, `opened_to` | ✓ | ✓ | ✓ |
| `open_at` (open at that instant) | ✓ | ✓ | ✓ |
| `opening_response_id`, `closing_response_id` | ✓ | ✓ | ✓ |
| `gtfs_static_version_id` | ✓ | ✓ | ✓ |
| `route_id` | | ✓ | ✓ |
| `cell_id`, `root_station_id`, `scheme_version` | ✓ | | |
| `recurrence` | ✓ | ✓ | ✓ |
| `rule_version_id`, `bounding_box_version_id`, `position_validation_reason`, `attribution_reason`, `expected_schedule_state` | | | ✓ |
| `agency_id=none` (unattributed) | | | ✓ |

`opened_from` and `opened_to` are needed because [`GET /v1/episode-counts`](#episode-counts) counts episodes that opened in the window, while `active_from` and `active_to` select episodes that overlap it. Without them, a group's count and its drill-down list would describe different sets of episodes.

`open_at` together with the cluster filters is what [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters)'s per-cluster links point to, which fixes [UC-05](#uc-05-geographic-concentrations-of-unmatched-stops).

### R5. Recurrence fields on every episode

Every episode representation gains two derived fields:

- `previous_episode_count`: retained episodes of the same identity that opened before this one. For a vehicle, this means the same vehicle and the same anomaly type.
- `previous_closed_at`: when the most recent of those closed, or `null` when there is none.

Together they answer "new or chronic?" ([N1](#n1-unmatched-stop-spike)), "reopened within minutes?" ([N2](#n2-oba-restart-or-reload)) and "how often?" ([N3](#n3-flapping-identifiers)) for every item on a page, with no request per identifier. The `recurrence` dimension and filter are computed from `previous_episode_count`.

Retention limits what these fields can say. "No previous episode" means none in the retained history, not none ever. Retention can also lower the count on an old closed episode, which changes its `ETag`. A day-old cached value is acceptable for a history count, so closed episodes keep `max-age=86400`.

### R6. Plain views as the single source for derived fields

Migration `00002`, which already adds the list indexes in [Section 7](investigation-api.md#7-schema-follow-ups-for-the-api), also adds three plain (not materialized) views:

- `unmatched_stop_episode_view`: the episode plus `opened_at`, `closed_at`, the opening and closing `response_id`, `stop_id`, server, the latest static context (`static_found`, `stop_metadata_version_id`, `gtfs_static_version_id`) and the [R5](#r5-recurrence-fields-on-every-episode) fields.
- `unmatched_trip_episode_view`: the same, plus `route_id`, `service_id` and `block_id` from the latest static context.
- `vehicle_anomaly_episode_view`: the episode plus `anomaly_code`, `endpoint_id`, `opened_at`, `closed_at`, the opening evidence fields named in [R3](#r3-grouped-counts-on-get-v1episode-counts) and the [R5](#r5-recurrence-fields-on-every-episode) fields.

Lists ([`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list), [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list), [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list)), single episodes ([`GET /v1/unmatched-stop-episodes/{episode_id}`](#one-unmatched-stop-episode), [`GET /v1/unmatched-trip-episodes/{episode_id}`](#one-unmatched-trip-episode), [`GET /v1/vehicle-anomaly-episodes/{episode_id}`](#one-vehicle-anomaly-episode)) and grouped counts ([`GET /v1/episode-counts`](#episode-counts)) all read these views. Each derived field is then defined once, and a filter and a grouping on the same dimension always agree.

Implementation notes:
- **Plain views, not materialized.** Open episodes change every collection interval, and windows are at most 31 days. A materialized view would be stale or need constant refreshing. Revisit only if [`GET /v1/episode-counts`](#episode-counts) comes close to the 5-second timeout ([D4](investigation-api.md#6-implementation-decisions)) in profiling.
- **Use `LATERAL` subqueries, not window functions, for the latest static context and the [R5](#r5-recurrence-fields-on-every-episode) fields.** PostgreSQL cannot push a filter such as `agency_id = $1` into a view that computes a window function over another partition, so it would compute the window over every episode. A `LATERAL` lookup per row uses the existing `unmatched_stop_episode_entity` and `stop_metadata_version_reported_stop` indexes.
- `cluster` is not in the view, because it depends on `scheme_version`. Queries join `stop_cluster_assignment` with the scheme as a parameter.

### R7. Data that is not stored stays out of the API

These answers belong in Prometheus or in a separate change, and the API's limits table ([Section 2.5](investigation-api.md#25-limits-of-the-evidence)) should say so:
- Fleet size, and OBA counts between stored polls: Prometheus.
- Whether Watchdog was recording: a Prometheus counter for failed history writes, so [N7](#n7-no-data-or-no-recording) can be answered from the metrics Watchdog already exports. This is a recording change, not an API change.

### Unchanged

- Include depth stays one level. With [R1](#r1-one-shared-included-section-compound-document), reaching a second-level resource such as a bounding box costs one request per distinct target, and in practice there are one or two.
- Small resources with links remain the base design. The rules above add to it and replace nothing except the include lists and the shape of `included`.

### A consistency issue found along the way

The list example in [Section 5](investigation-api.md#a-paginated-episode-list) embeds an `agency` object in every item and also links to it. With [R1](#r1-one-shared-included-section-compound-document) and [R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link), `include=agency` covers this. The proposal is to remove the inline object and keep `agency_id`, so each fact has one place in the response.

---

## 6. Rules checked against every scenario

Request counts in the typical case, excluding resolving the server and agency once. Costs use the same notation as the walkthroughs.

| Scenario | Today | With [R1](#r1-one-shared-included-section-compound-document)–[R6](#r6-plain-views-as-the-single-source-for-derived-fields) | How | Still not answerable |
|---|---|---|---|---|
| [UC-01](#uc-01-unmatched-trips-during-degraded-trip-matching) | pages + pages + pages + 3 | 2 + pages | `group_by=static_found`, `group_by=route_id`, list with includes | |
| [UC-02](#uc-02-reconstruct-unmatched-stops-after-an-alert-cleared) | pages + 5 | pages | List with `include=static_contexts,opening_response,closing_response` | |
| [UC-03](#uc-03-recurrence-of-one-stop-or-trip) | 2 | 2 | Unchanged | |
| [UC-04](#uc-04-static-gtfs-status-of-an-unmatched-identifier) | 3–5 | 2 | [`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`](#static-context-for-a-stop-episode) `include=gtfs_static_version`, [`GET /v1/static-gtfs-sources/{source_id}/versions`](#a-static-source-across-versions) | |
| [UC-05](#uc-05-geographic-concentrations-of-unmatched-stops) | not possible | 2 | [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters), then a cluster's `links.episodes` | |
| [UC-06](#uc-06-vehicle-with-an-outdated-position-or-not-shown) | 3 | 3 | Unchanged | |
| [UC-07](#uc-07-implausible-vehicle-positions) | pages + pages + 2 | 1 + pages + 2 | `group_by=position_validation_reason`, list, [`GET /v1/bounding-box-versions/{bounding_box_version_id}`](#one-bounding-box-version) | |
| [UC-08](#uc-08-feed-wide-freshness-degradation) | pages + pages + 3 | 2 + pages | `group_by=opening_response`, list with `include=endpoint,opening_response` | Fleet size (Prometheus) |
| [UC-09](#uc-09-effect-of-a-detection-rule-change) | 3 | 2 | [`GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions`](#rule-history-for-an-anomaly-type), `group_by=rule_version` | |
| [UC-10](#uc-10-periodic-data-quality-review) | 1 + one per identifier | 2 | [`GET /v1/episode-counts`](#episode-counts), `group_by=entity` | |
| [UC-11](#uc-11-incidents-across-a-feed-url-change) | 1 + one per endpoint | 1 | `group_by=endpoint` | |
| [UC-12](#uc-12-how-a-vehicle-anomaly-ended) | 1 | 1 | Unchanged | |
| [UC-13](#uc-13-effect-of-a-corrective-action) | 3 + one per identifier | 3 | [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list), open list, list with `opened_from` and `recurrence=has_previous_episode` | Proof that the fix caused the closures (as today) |
| [UC-14](#uc-14-incident-timeline) | 7 + many | 6 | Three lists, [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list), [`GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions`](#rule-history-for-an-anomaly-type) per type, `group_by=opened_hour` | |
| [N1](#n1-unmatched-stop-spike) | 7 + 600 | 6 | [`GET /v1/episode-counts`](#episode-counts), [`GET /v1/gtfs-static-versions`](#static-gtfs-version-list), [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters), `group_by=static_found`, `group_by=opening_response`, list with includes and [R5](#r5-recurrence-fields-on-every-episode) fields | |
| [N2](#n2-oba-restart-or-reload) | pages + one per identifier | 3 | `group_by=closing_response`, list filtered by `closing_response_id` showing `previous_closed_at`, [`GET /v1/oba-metrics-responses/{response_id}`](#one-oba-metrics-response) | OBA state between stored polls (Prometheus) |
| [N3](#n3-flapping-identifiers) | pages + one per identifier | 2 | `group_by=entity`, the identity's episode list | |
| [N4](#n4-server-wide-incident-across-agencies) | one per agency + pages per agency | 2 | `group_by=agency`, `group_by=opening_response` with `server_id` | |
| [N5](#n5-bounding-box-shrinks-after-a-static-update) | pages + 3 | 4 | `group_by=bounding_box_version`, [`GET /v1/bounding-box-versions/{bounding_box_version_id}`](#one-bounding-box-version) twice, [`GET /v1/gtfs-static-versions/{gtfs_static_version_id}`](#one-static-gtfs-version) | |
| [N6](#n6-vehicles-missing-from-an-agencys-view) | not possible | 2 | `group_by=attribution_reason` with `server_id`, list with `agency_id=none` | |
| [N7](#n7-no-data-or-no-recording) | not possible | not possible | | Recording health (Prometheus counter, [R7](#r7-data-that-is-not-stored-stays-out-of-the-api)) |

Every scenario except [N7](#n7-no-data-or-no-recording) now meets [B1](#b1-about-ten-requests-in-the-typical-case)–[B3](#b3-summary-questions-need-no-paging). [N7](#n7-no-data-or-no-recording) needs data the store does not keep, and [R7](#r7-data-that-is-not-stored-stays-out-of-the-api) gives it a separate home.

---

## 7. Decisions to confirm

1. **[R1](#r1-one-shared-included-section-compound-document).** One shared `included` object keyed by link URL, replacing per-item `included`.
2. **[R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link).** Includes derived from the link kind (every to-one link, plus to-many links to unpaginated collections), replacing the per-endpoint lists.
3. **[R3](#r3-grouped-counts-on-get-v1episode-counts).** `group_by` and `episode_type` on [`GET /v1/episode-counts`](#episode-counts), with the dimension list, one dimension per request, and the 200-group cap.
4. **[R4](#r4-every-dimension-is-also-a-filter).** The new list filters, including `opened_from`/`opened_to`, `open_at`, the cluster filters and `agency_id=none`.
5. **[R5](#r5-recurrence-fields-on-every-episode).** `previous_episode_count` and `previous_closed_at` on every episode, accepting that retention can lower the count and change a closed episode's `ETag`.
6. **[R6](#r6-plain-views-as-the-single-source-for-derived-fields).** Three plain views in migration `00002`, built with `LATERAL` lookups. Materialized views deferred until profiling shows a need.
7. **[R7](#r7-data-that-is-not-stored-stays-out-of-the-api).** Fleet size, OBA counts between stored polls and recording health stay outside the API. A failed-history-write counter is proposed as a separate recording change.
8. **Inline `agency`** removed from list items in favour of `agency_id` plus `include=agency`.
9. **New use cases.** [N1](#n1-unmatched-stop-spike)–[N7](#n7-no-data-or-no-recording) are added to Section 2 of the API design as UC-15 to UC-21, with their questions added to the question catalog.

## 8. Changes to the design docs once confirmed

| Document | Section | Change |
|---|---|---|
| `investigation-api.md` | 2.2–2.4 | Add UC-15 to UC-21 and their questions |
| | 2.5 | Add fleet size, OBA counts between polls and recording health to the limits table |
| | 3, Including related resources | Rewrite for [R1](#r1-one-shared-included-section-compound-document) and [R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link) |
| | 4.3, 4.4 | Replace the **Includes** entries with a pointer to [R2](#r2-which-links-can-be-included-follows-from-the-kind-of-link); add the [R4](#r4-every-dimension-is-also-a-filter) filters to [`GET /v1/unmatched-stop-episodes`](#unmatched-stop-episode-list), [`GET /v1/unmatched-trip-episodes`](#unmatched-trip-episode-list) and [`GET /v1/vehicle-anomaly-episodes`](#vehicle-anomaly-episode-list); add the [R5](#r5-recurrence-fields-on-every-episode) fields to [`GET /v1/unmatched-stop-episodes/{episode_id}`](#one-unmatched-stop-episode), [`GET /v1/unmatched-trip-episodes/{episode_id}`](#one-unmatched-trip-episode) and [`GET /v1/vehicle-anomaly-episodes/{episode_id}`](#one-vehicle-anomaly-episode); define [`GET /v1/unmatched-stop-clusters`](#unmatched-stop-clusters)'s per-cluster link |
| | 4.6 | Add `episode_type` and `group_by` to [`GET /v1/episode-counts`](#episode-counts) |
| | 5 | Update the examples for [R1](#r1-one-shared-included-section-compound-document), remove the inline `agency`, add a grouped-counts example |
| | 6 | Revise [D1](investigation-api.md#6-implementation-decisions); add decisions for [R3](#r3-grouped-counts-on-get-v1episode-counts)–[R6](#r6-plain-views-as-the-single-source-for-derived-fields) |
| | 7 | Add the views to migration `00002` |
| `historical-investigation-schema.md` | Views (new) | Describe the three views and their derived fields, matching migration `00002` |

---

## 9. Endpoint reference

Every request named in this review, in API order. The last column opens the endpoint's full description in the API design.

### Feed URLs a server has used

`GET /v1/servers/{server_id}/vehicle-position-endpoints`: Feed URLs a server has used. [API design §4.2](investigation-api.md#42-servers-agencies-and-feeds).

### Reported stop lookup

`GET /v1/reported-stops`: Look up a reported stop by `agency_id` and `stop_id`. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Episodes for one stop

`GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes`: Every retained episode for one stop. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Reported trip lookup

`GET /v1/reported-trips`: Look up a reported trip by `agency_id` and `trip_id`. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Episodes for one trip

`GET /v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes`: Every retained episode for one trip. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Unmatched-stop episode list

`GET /v1/unmatched-stop-episodes`: Unmatched-stop episodes, filtered and paged. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### One unmatched-stop episode

`GET /v1/unmatched-stop-episodes/{episode_id}`: One unmatched-stop episode. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Static context for a stop episode

`GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts`: Static GTFS context for one stop episode. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Unmatched-trip episode list

`GET /v1/unmatched-trip-episodes`: Unmatched-trip episodes, filtered and paged. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### One unmatched-trip episode

`GET /v1/unmatched-trip-episodes/{episode_id}`: One unmatched-trip episode. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Static context for a trip episode

`GET /v1/unmatched-trip-episodes/{episode_id}/static-contexts`: Static GTFS context for one trip episode. [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### One OBA metrics response

`GET /v1/oba-metrics-responses/{response_id}`: One stored OBA metrics response (a poll). [API design §4.3](investigation-api.md#43-unmatched-stops-and-trips).

### Vehicle lookup

`GET /v1/vehicles`: Look up a vehicle by `vehicle_id`. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### Episodes for one vehicle

`GET /v1/vehicles/{vehicle_entity_id}/anomaly-episodes`: Every retained anomaly episode for one vehicle. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### Vehicle anomaly episode list

`GET /v1/vehicle-anomaly-episodes`: Vehicle anomaly episodes, filtered and paged. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### One vehicle anomaly episode

`GET /v1/vehicle-anomaly-episodes/{episode_id}`: One vehicle anomaly episode. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### Evidence for a vehicle episode

`GET /v1/vehicle-anomaly-episodes/{episode_id}/evidence`: Evidence for one vehicle anomaly episode. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### One bounding box version

`GET /v1/bounding-box-versions/{bounding_box_version_id}`: One bounding box version. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### Rule history for an anomaly type

`GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions`: Rule history for one anomaly type. [API design §4.4](investigation-api.md#44-vehicle-anomalies).

### Static GTFS version list

`GET /v1/gtfs-static-versions`: Static GTFS versions Watchdog published. [API design §4.5](investigation-api.md#45-static-gtfs).

### One static GTFS version

`GET /v1/gtfs-static-versions/{gtfs_static_version_id}`: One static GTFS version. [API design §4.5](investigation-api.md#45-static-gtfs).

### A static source across versions

`GET /v1/static-gtfs-sources/{source_id}/versions`: One static source's content in each version. [API design §4.5](investigation-api.md#45-static-gtfs).

### Unmatched-stop clusters

`GET /v1/unmatched-stop-clusters`: Open unmatched stops at a time, grouped by area. [API design §4.6](investigation-api.md#46-summaries-and-clusters).

### Episode counts

`GET /v1/episode-counts`: Episode counts per type over a period. [API design §4.6](investigation-api.md#46-summaries-and-clusters).
