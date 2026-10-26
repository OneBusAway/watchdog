# Watchdog Investigation REST API

**Status:** Design. Not implemented yet.
**Builds on:** [ADR 0001](adr/0001-durable-historical-investigation-storage.md) and the [historical investigation schema](historical-investigation-schema.md).

This document defines the read-only HTTP API that investigators and OBACloud use to query Watchdog's PostgreSQL investigation history. Section 1 lists every supported endpoint. Section 2 defines the investigation use cases and the questions the stored data can answer. Section 3 defines the conventions all endpoints follow, and Section 4 describes each endpoint. Resource and URI design follows the REST API Design Rulebook (chapters 1–4).

---

## 1. Supported endpoints

The investigation API consists of **exactly the 41 endpoints listed in this section**:

- Every endpoint supports `GET` and `HEAD` and returns JSON.
- The API is read-only. It has no endpoint that creates, changes or deletes data.
- A path that is not listed here is not part of the API and returns `404 Not Found`. A method other than `GET`, `HEAD` or `OPTIONS` returns `405 Method Not Allowed`.
- Every endpoint requires a bearer token and is available only when the investigation database is enabled (see [Section 3](#3-conventions)).

Each endpoint has a stable identifier (E01–E41). [Section 2](#2-investigation-use-cases) explains which investigations the endpoints support, [Section 3](#3-conventions) defines the rules common to all of them, and [Section 4](#4-endpoint-reference) describes each endpoint in detail.

**Entry point**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E01 | `GET /v1/investigation` | The API root: links to every top-level collection | All |

**Servers, agencies and feeds**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E02 | `GET /v1/servers` | Monitored OBA servers | UC-11, UC-15 |
| E03 | `GET /v1/servers/{server_id}` | One server | UC-11 |
| E04 | `GET /v1/servers/{server_id}/agencies` | Agencies seen on a server | UC-10 |
| E05 | `GET /v1/agencies/{agency_id}` | One agency | UC-10 |
| E06 | `GET /v1/servers/{server_id}/vehicle-position-endpoints` | Vehicle-position feed URLs a server has used | UC-08, UC-11 |
| E07 | `GET /v1/vehicle-position-endpoints/{endpoint_id}` | One feed URL | UC-08, UC-11 |

**Unmatched stops and trips**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E08 | `GET /v1/reported-stops` | Reported stops, looked up by `agency_id` and `stop_id` | UC-03, UC-14 |
| E09 | `GET /v1/reported-stops/{reported_stop_id}` | One reported stop | UC-03 |
| E10 | `GET /v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes` | Every unmatched episode for that stop | UC-03, UC-14 |
| E11 | `GET /v1/reported-trips` | Reported trips, looked up by `agency_id` and `trip_id` | UC-03, UC-14 |
| E12 | `GET /v1/reported-trips/{reported_trip_id}` | One reported trip | UC-03 |
| E13 | `GET /v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes` | Every unmatched episode for that trip | UC-03, UC-14 |
| E14 | `GET /v1/unmatched-stop-episodes` | Unmatched-stop episodes, filtered and paginated | UC-02, UC-12, UC-14, UC-15 |
| E15 | `GET /v1/unmatched-stop-episodes/{episode_id}` | One unmatched-stop episode | UC-02, UC-04 |
| E16 | `GET /v1/unmatched-stop-episodes/{episode_id}/static-contexts` | What Watchdog's static GTFS said about the stop during the episode | UC-02, UC-04, UC-14 |
| E17 | `GET /v1/unmatched-trip-episodes` | Unmatched-trip episodes, filtered and paginated | UC-01, UC-12, UC-14, UC-15 |
| E18 | `GET /v1/unmatched-trip-episodes/{episode_id}` | One unmatched-trip episode | UC-01, UC-04 |
| E19 | `GET /v1/unmatched-trip-episodes/{episode_id}/static-contexts` | What Watchdog's static GTFS said about the trip during the episode | UC-01, UC-04 |
| E20 | `GET /v1/oba-metrics-responses/{response_id}` | An OBA metrics response that opened or closed an episode | UC-12 |

**Vehicle anomalies**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E21 | `GET /v1/vehicles` | Vehicles, looked up by `vehicle_id` with `endpoint_id` or `server_id` | UC-06 |
| E22 | `GET /v1/vehicles/{vehicle_entity_id}` | One vehicle | UC-06 |
| E23 | `GET /v1/vehicles/{vehicle_entity_id}/anomaly-episodes` | Every anomaly episode for that vehicle | UC-06, UC-14 |
| E24 | `GET /v1/vehicle-anomaly-episodes` | Vehicle anomaly episodes, filtered and paginated | UC-06, UC-07, UC-08, UC-11, UC-15 |
| E25 | `GET /v1/vehicle-anomaly-episodes/{episode_id}` | One vehicle anomaly episode | UC-06, UC-13 |
| E26 | `GET /v1/vehicle-anomaly-episodes/{episode_id}/evidence` | Opening, change and resolution evidence for the episode | UC-06, UC-07, UC-13 |
| E27 | `GET /v1/vehicle-position-evidence/{evidence_id}` | One evidence record | UC-07 |
| E28 | `GET /v1/vehicle-position-responses/{response_id}` | One stored vehicle-position response | UC-08, UC-13 |
| E29 | `GET /v1/bounding-box-versions/{bounding_box_version_id}` | The bounding box applied in a check | UC-07 |
| E30 | `GET /v1/vehicle-anomaly-types` | Vehicle anomaly types | UC-09 |
| E31 | `GET /v1/vehicle-anomaly-types/{anomaly_code}` | One anomaly type | UC-09 |
| E32 | `GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions` | Rule history of one anomaly type | UC-09, UC-15 |
| E33 | `GET /v1/vehicle-anomaly-rule-versions/{rule_version_id}` | One rule version | UC-07, UC-09 |

**Static GTFS**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E34 | `GET /v1/gtfs-static-versions` | Static GTFS versions Watchdog published | UC-04, UC-14, UC-15 |
| E35 | `GET /v1/gtfs-static-versions/{gtfs_static_version_id}` | One static version, with its sources and agencies | UC-04, UC-14 |
| E36 | `GET /v1/static-gtfs-sources/{source_id}` | One static GTFS source URL | UC-04 |
| E37 | `GET /v1/static-gtfs-sources/{source_id}/versions` | That source's content fingerprint in each version | UC-04 |

**Summaries and clusters**

| ID | Endpoint | Returns | Use cases |
|---|---|---|---|
| E38 | `GET /v1/unmatched-stop-clusters` | Open unmatched stops at a given time, grouped by station and S2 cell | UC-05 |
| E39 | `GET /v1/cluster-schemes` | Stop-clustering rule sets | UC-05 |
| E40 | `GET /v1/cluster-schemes/{scheme_version}` | One clustering rule set | UC-05 |
| E41 | `GET /v1/episode-counts` | Episodes opened, closed and still open per type over a period | UC-09, UC-10, UC-15 |

---

## 2. Investigation use cases

### 2.1 Purpose and terms

Watchdog observes OneBusAway (OBA) servers and their GTFS-Realtime feeds every collection interval, 30 seconds by default. Both sources expose only their current state. OBA's `metrics.json` lists the identifiers that are unmatched now, and a vehicle-position feed lists the vehicles it reports now. When a problem is investigated hours or days later, that state has usually changed. The investigation history stored in PostgreSQL, and this API over it, exist to answer questions about that earlier state.

This section defines the use cases the API must support. Each use case states what prompts the investigation, what the observer wants to learn, which questions the stored data can answer, how to answer them through the API, and what the data cannot establish. The questions are listed with stable identifiers (Q01–Q27) in [Section 2.4](#24-question-catalog), and each use case refers to them.

| Term | Meaning |
|---|---|
| **Observer** | A person investigating a data-quality problem after the fact (see actors below). |
| **Episode** | A continuous period during which Watchdog observed a condition: an identifier reported as unmatched by OBA, or a vehicle anomaly. An episode opens on the first observation of the condition and closes on the first later successful observation without it. |
| **Evidence** | The values Watchdog recorded from a vehicle-position response when a vehicle anomaly opened, changed meaningfully, or closed. |
| **Static context** | What Watchdog's own copy of the static GTFS said about a reported stop or trip. It is not proof of the data OBA used. |
| **Observation time** | The time Watchdog made an observation. Episode boundaries are observation times, not the exact moment a condition began or ended. |

| Actor | Role |
|---|---|
| **Operations operator** | Monitors OBA deployments through OBACloud and responds to alerts. |
| **Agency data maintainer** | Maintains an agency's static GTFS and GTFS-Realtime feeds and fixes the data problems the investigation reveals. |
| **Watchdog maintainer** | Maintains Watchdog's detection rules and verifies their behaviour. |

### 2.2 Use case summary

| ID | Use case | Primary actor | Typical trigger |
|---|---|---|---|
| UC-01 | Identify unmatched trips during a period of degraded trip matching | Operations operator | The trip-match ratio for an agency fell during a past period |
| UC-02 | Reconstruct unmatched stops after an alert has cleared | Operations operator | An unmatched-stop alert fired and later resolved before review |
| UC-03 | Assess the recurrence of an unmatched stop or trip | Agency data maintainer | The same identifier is reported as unmatched repeatedly |
| UC-04 | Determine the static GTFS status of an unmatched identifier | Agency data maintainer | An unmatched stop or trip needs to be checked against static GTFS |
| UC-05 | Locate geographic concentrations of unmatched stops | Agency data maintainer | Unmatched stops appear to concentrate in one area or station |
| UC-06 | Investigate a vehicle shown with an outdated position or not shown | Operations operator | A report that a vehicle disappeared or stopped updating at a known time |
| UC-07 | Investigate implausible vehicle positions | Agency data maintainer | Vehicles reported at invalid coordinates or outside the service area |
| UC-08 | Attribute a feed-wide freshness degradation | Operations operator | Aggregate source-timestamp age rose across a feed |
| UC-09 | Assess the effect of a detection rule change | Watchdog maintainer | A detection threshold or rule changed in a deployment |
| UC-10 | Conduct a periodic data-quality review | Agency data maintainer | A scheduled weekly or monthly review |
| UC-11 | Separate incidents across a feed URL change | Operations operator | A vehicle-position feed URL changed in the configuration |
| UC-12 | Compare unmatched reports between OBA implementations | Watchdog maintainer | Java OBA and Maglev classify the same data differently |
| UC-13 | Determine how a vehicle anomaly ended | Operations operator | A vehicle anomaly is closed and its outcome must be confirmed |
| UC-14 | Verify the effect of a corrective action | Agency data maintainer | A corrected GTFS or feed fix was published |
| UC-15 | Prepare an incident timeline | Operations operator | A post-incident review requires a factual timeline |

### 2.3 Use cases

#### UC-01: Identify unmatched trips during a period of degraded trip matching

- **Trigger:** Prometheus shows that an agency's trip-match ratio fell during a past period. The current `metrics.json` no longer lists the affected trip IDs.
- **Objective:** Identify which trip IDs OBA reported as unmatched during that period, and gather the static context needed to direct a fix.
- **Questions answered:** Q01, Q02, Q03, Q08, Q10.
- **Procedure:**
  1. `GET /v1/unmatched-trip-episodes` with `agency_id`, `active_from` and `active_to` set to the degraded period.
  2. For each episode, read `opened_at`, `last_reported_at`, `closed_at` and `duration_seconds`.
  3. Follow `links.static_contexts` to see whether each trip was found in Watchdog's static GTFS and, if so, its route, service and block.
  4. Use `static_found=false` to separate trips absent from static GTFS from trips that exist but were still unmatched.
- **Value to the observer:** Turns a drop in an aggregate ratio into a specific, time-bounded list of trip IDs, already split by likely cause: missing from static GTFS, or present but unmatched.
- **Limitations:** The API reports OBA's classification and Watchdog's static context. It does not state why OBA could not match a trip.

#### UC-02: Reconstruct unmatched stops after an alert has cleared

- **Trigger:** An unmatched-stop alert fired outside working hours and cleared before anyone reviewed it.
- **Objective:** Establish which stops were unmatched, for how long, and how each episode ended.
- **Questions answered:** Q01, Q02, Q03, Q09.
- **Procedure:**
  1. `GET /v1/unmatched-stop-episodes` with `server_id` or `agency_id` and a window covering the alert.
  2. Read each episode's boundaries and `close_reason`.
  3. Follow `links.static_contexts` for the stop name and location.
- **Value to the observer:** Preserves the evidence of a transient condition that is no longer visible upstream, so the alert can be reviewed rather than dismissed.
- **Limitations:** `close_reason: "not_reported"` means OBA stopped listing the stop. It does not confirm that the stop became matched.

#### UC-03: Assess the recurrence of an unmatched stop or trip

- **Trigger:** The same stop or trip identifier is observed as unmatched on several occasions.
- **Objective:** Determine whether the condition is persistent, intermittent, or recurring at regular times.
- **Questions answered:** Q04, Q02, Q03, Q05.
- **Procedure:**
  1. `GET /v1/reported-stops?agency_id=&stop_id=` (or `/v1/reported-trips?agency_id=&trip_id=`) to resolve the identifier.
  2. Follow `links` to its episodes collection, which lists every retained episode for that identifier.
  3. Compare the episodes' start times and durations.
- **Value to the observer:** Distinguishes a one-off event from a structural problem. A pattern such as an episode at the same hour every day points to a schedule- or feed-generation cause rather than a data error.
- **Limitations:** History is limited to the retention period (90 days for closed episodes).

#### UC-04: Determine the static GTFS status of an unmatched identifier

- **Trigger:** An agency data maintainer needs to know whether an unmatched stop or trip exists in static GTFS, and whether a static update changed it.
- **Objective:** Establish what Watchdog's static GTFS said about the identifier throughout the episode.
- **Questions answered:** Q08, Q09, Q10, Q11, Q12, Q13.
- **Procedure:**
  1. `GET /v1/unmatched-stop-episodes/{id}/static-contexts` (or the trip equivalent).
  2. Each item states whether the identifier was found, its static attributes, and the period it applies to (`applies_from`, `applies_until`).
  3. Follow `links.gtfs_static_version` to see when that version was published and which source files it contained.
  4. `GET /v1/static-gtfs-sources/{id}/versions` shows whether a source's content changed between versions.
- **Value to the observer:** Separates two cases with different remedies: an identifier missing from static GTFS (correct the static data) and an identifier present but still unmatched (investigate the realtime data or OBA's processing). It also shows whether OBA continued to report an identifier after Watchdog's static GTFS already contained it, which suggests OBA had not yet loaded the newer data.
- **Limitations:** Static context reflects Watchdog's copy of the data. OBA does not report which static GTFS it loaded, so the two cannot be proven identical.

#### UC-05: Locate geographic concentrations of unmatched stops

- **Trigger:** Unmatched stops appear to concentrate around a station or area, often after a static GTFS update.
- **Objective:** Identify the stations and areas with the most unmatched stops at a given time.
- **Questions answered:** Q14, Q15, Q09.
- **Procedure:**
  1. `GET /v1/unmatched-stop-clusters?server_id=&at=` for the time of interest.
  2. Each item gives the S2 cell, the root station when known, and the number of open episodes, with links to those episodes.
  3. `GET /v1/cluster-schemes/{scheme_version}` documents the grouping rules used.
- **Value to the observer:** Directs attention to the station or area where a single correction may resolve many reports.
- **Limitations:** Grouping depends on the stop's static coordinates and parent station. Stops without static context are grouped as `unassigned`.

#### UC-06: Investigate a vehicle shown with an outdated position or not shown

- **Trigger:** A report states that a vehicle disappeared from, or stopped updating in, rider-facing applications at a known time.
- **Objective:** Establish what the vehicle-position feed reported for that vehicle around that time and whether the vehicle's trip was scheduled to be in service.
- **Questions answered:** Q16, Q17, Q18, Q19, Q23, Q24.
- **Procedure:**
  1. `GET /v1/vehicles?server_id=&vehicle_id=` to resolve the vehicle.
  2. Follow `links` to its anomaly episodes, filtered by `anomaly_type=stale_source_timestamp` and a window around the reported time.
  3. Follow `links.evidence` to read the source timestamp the feed carried, Watchdog's observation time, the trip descriptor, and `expected_schedule_state` with its scheduled window.
  4. Read `close_reason` to see whether the episode ended with fresh data or because the vehicle left a complete feed.
- **Value to the observer:** Distinguishes three cases:
  - the feed carried outdated positions during scheduled service (a feed or device problem);
  - the vehicle's trip had already ended and the feed kept a residual entry (expected behaviour);
  - the vehicle never showed an anomaly (look elsewhere, for example the rider-facing application).
- **Limitations:** The data does not establish the physical state of the vehicle or its equipment. It records only what the feed reported.

#### UC-07: Investigate implausible vehicle positions

- **Trigger:** Vehicles are displayed at invalid coordinates or outside the agency's service area.
- **Objective:** Identify the affected vehicles, the exact values received, and the validation rule or boundary that flagged them.
- **Questions answered:** Q16, Q19, Q20, Q21, Q22.
- **Procedure:**
  1. `GET /v1/vehicle-anomaly-episodes` with `anomaly_type=invalid_coordinate` or `anomaly_type=stopped_out_of_bounds` and a window.
  2. Follow `links.evidence` for the raw coordinates and `position_validation_reason`.
  3. For out-of-bounds episodes, follow `links.bounding_box_version` on the evidence for the four limits actually applied.
- **Value to the observer:** Provides the exact values to give to the feed producer, and confirms whether the boundary was correct. An unexpectedly small box points to a static GTFS problem rather than a realtime one.
- **Limitations:** Raw values are stored as received. The data does not identify the device or process that produced them.

#### UC-08: Attribute a feed-wide freshness degradation

- **Trigger:** Aggregate source-timestamp age metrics show a rise across a feed.
- **Objective:** Identify which vehicles were affected and which feed URL served them.
- **Questions answered:** Q16, Q18, Q25, Q26.
- **Procedure:**
  1. `GET /v1/vehicle-anomaly-episodes?anomaly_type=stale_source_timestamp&endpoint_id=` for the period, or `status=open` during the incident.
  2. Follow `links.endpoint` to confirm the feed URL.
  3. Follow `links.opening_response` to check whether the feed was a full snapshot or a differential update.
- **Value to the observer:** Determines whether a degradation affects a few vehicles (device-level) or most of the fleet (feed-level), which decides who should be contacted.
- **Limitations:** Aggregate trends remain in Prometheus. The API provides the entity-level detail behind them.

#### UC-09: Assess the effect of a detection rule change

- **Trigger:** A detection threshold or rule changed in a deployment, and episode volumes changed.
- **Objective:** Determine which rule version produced each episode and when each version took effect.
- **Questions answered:** Q22, Q27.
- **Procedure:**
  1. `GET /v1/vehicle-anomaly-types/{anomaly_code}/rule-versions` for the version history.
  2. Each episode links to the `rule_version` it was evaluated under.
  3. Compare `GET /v1/episode-counts` for periods before and after the change.
- **Value to the observer:** Separates a real change in data quality from a change in detection rules.
- **Limitations:** Episodes are never re-evaluated under a newer rule.

#### UC-10: Conduct a periodic data-quality review

- **Trigger:** A scheduled weekly or monthly review of an agency's data quality.
- **Objective:** Summarize how many episodes of each type opened, how they closed, and how many remain open.
- **Questions answered:** Q27, Q05, Q25.
- **Procedure:**
  1. `GET /v1/episode-counts?agency_id=&from=&to=` for the review period.
  2. Drill into any type of interest through the matching episode collection.
- **Value to the observer:** Provides a consistent, comparable summary for trend review, with direct links to the underlying detail.
- **Limitations:** Counts cover only the retention period and the anomaly types currently detected.

#### UC-11: Separate incidents across a feed URL change

- **Trigger:** A vehicle-position feed URL changed in the configuration.
- **Objective:** Attribute each incident to the URL that actually supplied it.
- **Questions answered:** Q18, Q16.
- **Procedure:**
  1. `GET /v1/servers/{id}/vehicle-position-endpoints` lists every URL the server has used, including ones no longer configured.
  2. Filter `GET /v1/vehicle-anomaly-episodes` by each `endpoint_id`.
- **Value to the observer:** Prevents incidents from an old feed being attributed to its replacement, which matters when a provider migration is being evaluated.
- **Limitations:** A changed URL is treated as a different endpoint. Watchdog does not record that two URLs served the same logical feed.

#### UC-12: Compare unmatched reports between OBA implementations

- **Trigger:** Java OBA and Maglev classify the same data differently.
- **Objective:** Identify which implementation produced each report and the counts it returned.
- **Questions answered:** Q07, Q06, Q01.
- **Procedure:**
  1. Filter unmatched episodes with `source_implementation=java` or `source_implementation=maglev`.
  2. Follow `links.opening_response` and `links.closing_response` for the implementation, its version, and the per-agency counts.
- **Value to the observer:** Supports a factual comparison of the two implementations' classifications, using each implementation's documented definition of "unmatched" (see the schema document).
- **Limitations:** The implementation is recorded only when Watchdog could determine it. Responses are stored only when they open or close an episode.

#### UC-13: Determine how a vehicle anomaly ended

- **Trigger:** A vehicle anomaly episode is closed, and the observer must establish whether the vehicle recovered.
- **Objective:** Determine the closing condition and the values observed at closure.
- **Questions answered:** Q17, Q19, Q26.
- **Procedure:**
  1. `GET /v1/vehicle-anomaly-episodes/{id}` and read `close_reason`.
  2. Follow `links.evidence` for the `resolution` evidence.
- **Value to the observer:** Distinguishes recovery (`fresh_source_timestamp`, `coordinates_valid_again`, `inside_bounding_box`) from removal (`not_present_in_full_dataset`), which may simply mean the vehicle's trip ended.
- **Limitations:** Close reasons describe the observed condition, not a physical cause.

#### UC-14: Verify the effect of a corrective action

- **Trigger:** An agency published a corrected static GTFS or fixed a realtime feed.
- **Objective:** Confirm that the related reports stopped after the correction and have not recurred.
- **Questions answered:** Q05, Q02, Q11, Q04, Q24.
- **Procedure:**
  1. `GET /v1/unmatched-stop-episodes?status=open` (or the trip or vehicle equivalent) to confirm no related episodes remain open.
  2. For episodes that closed after the correction, compare `closed_at` with the `published_at` of the new static version linked from their static contexts.
  3. Check the identifier's episode history (UC-03) over the following days for recurrence.
- **Value to the observer:** Gives an evidence-based confirmation that a fix was effective, instead of relying on the absence of new alerts.
- **Limitations:** An episode closing shortly after a correction is consistent with the correction working, but the data alone does not prove causation.

#### UC-15: Prepare an incident timeline

- **Trigger:** A post-incident review requires a factual sequence of events.
- **Objective:** Assemble a timeline of the episodes, static GTFS publications and rule changes during the incident.
- **Questions answered:** Q01, Q02, Q12, Q16, Q17, Q22, Q27.
- **Procedure:**
  1. List the relevant episode collections for the incident window.
  2. `GET /v1/gtfs-static-versions?server_id=&published_from=&published_to=` for static publications in the same window.
  3. Include rule versions whose `effective_at` falls in the window.
  4. Use `GET /v1/episode-counts` for the summary figures.
- **Value to the observer:** Produces a timeline built from recorded observations, suitable for an incident report.
- **Limitations:** All times are Watchdog observation times. The timeline shows when Watchdog observed changes, not when they occurred upstream.

### 2.4 Question catalog

Each question below can be answered from the stored data. The use cases above refer to these identifiers.

**Unmatched stops and trips**

| ID | Question | Resource |
|---|---|---|
| Q01 | Which stop or trip IDs did OBA report as unmatched for a server or agency during a time range? | `GET /v1/unmatched-stop-episodes`, `GET /v1/unmatched-trip-episodes` with `agency_id`, `active_from`, `active_to` |
| Q02 | When was each identifier first reported, last reported, and no longer reported? | Episode document: `opened_at`, `last_reported_at`, `closed_at` |
| Q03 | How long did each unmatched period last? | Episode document: `duration_seconds` |
| Q04 | How often has the same identifier become unmatched again? | `GET /v1/reported-stops/{id}/unmatched-stop-episodes`, `GET /v1/reported-trips/{id}/unmatched-trip-episodes` |
| Q05 | Which identifiers are unmatched now? | Episode collections with `status=open` |
| Q06 | What unmatched and matched counts did the responses that opened or closed an episode contain? | `GET /v1/oba-metrics-responses/{id}` |
| Q07 | Which OBA implementation produced the report? | `GET /v1/oba-metrics-responses/{id}`; filter `source_implementation` |

**Static GTFS context**

| ID | Question | Resource |
|---|---|---|
| Q08 | Was the identifier present in Watchdog's static GTFS? | `…/static-contexts` (`static_stop_found`, `static_trip_found`); filter `static_found` |
| Q09 | What were the stop's name, coordinates, parent station and location type? | `GET /v1/unmatched-stop-episodes/{id}/static-contexts` |
| Q10 | What route, service and block did an unmatched trip belong to? | `GET /v1/unmatched-trip-episodes/{id}/static-contexts` |
| Q11 | Did the static context change while the episode was open, and when? | `…/static-contexts` (items with `applies_from` and `applies_until`) |
| Q12 | Which static GTFS version was loaded, when was it published, and which sources did it contain? | `GET /v1/gtfs-static-versions/{id}` |
| Q13 | Did a static source's content change between versions? | `GET /v1/static-gtfs-sources/{id}/versions` |
| Q14 | Where were unmatched stops concentrated at a given time? | `GET /v1/unmatched-stop-clusters` |
| Q15 | Which clustering rules produced that grouping? | `GET /v1/cluster-schemes/{scheme_version}` |

**Vehicle anomalies**

| ID | Question | Resource |
|---|---|---|
| Q16 | Which vehicles had anomalies of a given type during a time range? | `GET /v1/vehicle-anomaly-episodes` with `anomaly_type` and a window |
| Q17 | When did each anomaly begin and end, and what condition closed it? | Vehicle anomaly episode document |
| Q18 | Which vehicle-position feed URL reported it? | `GET /v1/vehicle-position-endpoints/{id}` |
| Q19 | What did the vehicle report when the anomaly opened, changed and closed? | `GET /v1/vehicle-anomaly-episodes/{id}/evidence` |
| Q20 | Why were the coordinates considered invalid? | Evidence `position_validation_reason` |
| Q21 | Which bounding box was applied? | `GET /v1/bounding-box-versions/{id}` |
| Q22 | Which rule and threshold flagged the anomaly, and since when was that rule in effect? | `GET /v1/vehicle-anomaly-rule-versions/{id}` |
| Q23 | Was the vehicle's trip scheduled to be in service, and in what window? | Evidence `expected_schedule_state`, `expected_schedule_reason`, `expected_window_start`, `expected_window_end` |
| Q24 | Has the same vehicle had the same anomaly again? | `GET /v1/vehicles/{id}/anomaly-episodes` |
| Q25 | Which vehicles have an anomaly open now? | `GET /v1/vehicle-anomaly-episodes?status=open` |
| Q26 | Was the feed response a full snapshot or a differential update? | `GET /v1/vehicle-position-responses/{id}` (`incrementality`) |

**Summaries**

| ID | Question | Resource |
|---|---|---|
| Q27 | How many episodes of each type opened, closed (by reason) and remain open over a period? | `GET /v1/episode-counts` |

Q06 is limited by design. Watchdog stores an OBA metrics response only when it opens or closes an episode, not on every poll. Continuous count history remains in Prometheus.

### 2.5 Limits of the evidence

The following cannot be established from the stored data. The API must not present information that implies otherwise.

| Not established | Reason |
|---|---|
| That an identifier became matched or was fixed | OBA lists unmatched identifiers but reports only counts of matched ones. An episode closes as `not_reported`. |
| Why OBA classified an identifier as unmatched | Only what OBA reported is stored; Watchdog does not infer a cause. |
| Which static GTFS OBA used | OBA does not report its static GTFS version. Static context is Watchdog's copy. |
| What happened between observations | Episode boundaries are observation times. |
| The physical cause of a vehicle anomaly | Evidence records feed values, not vehicle or equipment state. |
| A vehicle's complete path or position history | Only anomaly evidence is stored, not every position. |
| When vehicles left the feed, or when PostgreSQL was unavailable | Both are deferred in ADR 0001. |
| Anything before the investigation store was deployed | Earlier Prometheus history is not imported. |


---

## 3. Conventions

### Resources and URIs

- All resources live under the existing `/v1/` prefix, alongside `GET /v1/healthcheck` (`internal/app/routes.go`).
- **Collections** are plural nouns (`/v1/vehicles`); **documents** are a collection plus an ID (`/v1/vehicles/{id}`). Sub-collections express ownership (`/v1/vehicles/{id}/anomaly-episodes`). The API has no stores or controllers.
- Path segments are lowercase and hyphenated. There are no trailing slashes, verbs, or file extensions such as `.json`.
- **IDs in paths are internal database IDs** (`vehicle_entity_id`, `unmatched_stop_episode_id`, …). Clients treat URIs as opaque and follow `links` rather than building them.
- **External identifiers** (OBA stop and trip IDs, GTFS-Realtime vehicle IDs, agency IDs) are query filters, because they are only unique within a scope: an agency or a feed.
- **Query parameters and JSON fields use `snake_case`**, matching the schema and the existing healthcheck JSON. Times are RFC 3339 in UTC, for example `2026-10-24T14:05:30Z`.

### Methods

- The API is read-only. Every route supports `GET` and `HEAD`; Watchdog alone writes the data.
- `httprouter` does not add `HEAD` automatically, so each route is registered for both methods. Go's `net/http` drops the body for `HEAD`.
- `OPTIONS` is answered by `httprouter`'s default handling, with an `Allow: GET, HEAD, OPTIONS` header.
- Any other method returns `405 Method Not Allowed` with the same `Allow` header (also `httprouter`'s default).

### Authentication

- Clients send `Authorization: Bearer <token>`. Watchdog compares the token with the `INVESTIGATION_API_TOKEN` environment variable in constant time.
- When `INVESTIGATION_API_TOKEN` is unset, the investigation routes are not registered, so they return `404`. An open-source deployment exposes no entity history by accident.
- There is one shared token. OBACloud is the trusted client: it authenticates its own users and checks their server and agency permissions before it calls Watchdog. Watchdog does not know about users, so it never returns `403`.
- The token is never logged.

### Status codes

| Code | When |
|---|---|
| `200 OK` | Success. |
| `304 Not Modified` | `If-None-Match` matches the current `ETag`. |
| `400 Bad Request` | Invalid or unknown query parameter, an invalid cursor, a filter combination the API requires (see Query limits), or a time window over the limit. |
| `401 Unauthorized` | Missing or wrong bearer token. The response includes `WWW-Authenticate: Bearer realm="watchdog-investigation"`. |
| `404 Not Found` | No resource for that URI, including an ID whose episode has expired under the 90-day retention. |
| `405 Method Not Allowed` | Any method other than `GET`, `HEAD`, `OPTIONS`. Includes `Allow`. |
| `406 Not Acceptable` | The `Accept` header allows neither `application/json` nor `*/*`. |
| `500 Internal Server Error` | Unexpected failure; reported to Sentry. |
| `503 Service Unavailable` | The investigation database is disabled or was unavailable at startup (`Application.InvestigationDB` is nil). |

### Media types and errors

- Successful responses are `Content-Type: application/json`.
- Errors use `application/problem+json` (RFC 9457) with `type`, `title`, `status`, `detail`, and, for `400`, `invalid_params`:

```json
{
  "type": "https://github.com/OneBusAway/watchdog/blob/main/docs/investigation-api.md#query-limits",
  "title": "Invalid query parameters",
  "status": 400,
  "detail": "The time window may be at most 31 days.",
  "invalid_params": [
    { "name": "active_to", "reason": "must be within 31 days of active_from" }
  ]
}
```

### Pagination

- Collections use cursor pagination, with `page_size` (default `50`, maximum `200`) and an opaque `cursor`.
- Episode collections are ordered by episode ID descending. Episodes are inserted when they open, so this is newest-opened first, and the order stays stable while new episodes arrive.
- A page has `items` and `links`. `links.next` is present only when another page exists, and clients follow it as is.
- There is no total count, because counting large filtered sets is expensive. `GET /v1/episode-counts` provides totals.

### Query limits

- **Time window:** `active_from`/`active_to` select episodes whose open period overlaps the window. An open episode counts as active until now.
  - When `status=open`, the window is optional.
  - Otherwise it defaults to the last 24 hours, and `active_to - active_from` may be at most 31 days.
- **Scoped filters:**
  - `stop_id` and `trip_id` require `agency_id`.
  - `vehicle_id` requires `endpoint_id` or `server_id`.
- **Clusters:** `/v1/unmatched-stop-clusters` requires `server_id` and `at`.
- **Counts:** `/v1/episode-counts` requires `from` and `to`, at most 31 days apart.
- **Unknown parameters** are rejected with `400`, so a typo never silently returns an unfiltered list.

### Caching and conditional requests

`middleware.SecurityHeaders` sets `Cache-Control: no-store` on every response before the handler runs. Investigation handlers replace it with the following:

| Representation | `Cache-Control` | Validators |
|---|---|---|
| Closed episodes, their static contexts and evidence, static versions, rule versions, bounding boxes, responses | `private, max-age=86400` (they never change) | Strong `ETag`; `Last-Modified` = `closed_at` or the record's own time |
| Open episodes, collections, clusters, counts | `private, max-age=30` (one collection interval) | Strong `ETag` |

- `private` keeps shared caches from storing authenticated data.
- The `ETag` is a hash of the response body, and a matching `If-None-Match` returns `304`.

### Hypermedia links

Every representation has a `links` object of absolute-path URIs:
- `self` always.
- `next` on paged collections.
- Related resources by name, for example `server`, `agency`, `reported_stop`, `opening_response`, `closing_response`, `static_contexts`, `evidence`, `vehicle`, `endpoint`, `rule_version`, `bounding_box_version`, `gtfs_static_version`.

A link is omitted when the target does not exist, such as `closing_response` on an open episode.

### Wording rules

These carry over from the ADR and the schema document:
- Every episode has `status`: `open` or `closed`.
- Unmatched episodes expose `close_reason: "not_reported"`. No representation has `resolved`, `fixed`, or `matched` fields.
- Static context is labelled `"source": "watchdog_static_gtfs"`. It is what Watchdog's static GTFS said, not proof of what OBA used.
- `opened_at` and `closed_at` are Watchdog observation times, not the moment a condition actually began or ended.
- Vehicle close reasons describe the observed closing condition, such as `not_present_in_full_dataset`, never a cause.

---

## 4. Endpoint reference

This section describes every endpoint listed in [Section 1](#1-supported-endpoints), using the same identifiers. The **Source tables** column names the tables in `internal/db/migrations/00001_create_investigation_schema.sql`. Collection endpoints accept `page_size` and `cursor` in addition to the filters listed.

### 4.1 Entry point

| ID | Endpoint | Description |
|---|---|---|
| E01 | `/v1/investigation` | API root document. Its `links` name every top-level collection. Clients start here and follow links. |

### 4.2 Servers, agencies and feeds

| ID | Endpoint | Description | Source tables |
|---|---|---|---|
| E02 | `/v1/servers` | Monitored OBA servers. | `onebusaway_server` |
| E03 | `/v1/servers/{server_id}` | One server: `normalized_base_url`, `display_name`, `created_at`. | `onebusaway_server` |
| E04 | `/v1/servers/{server_id}/agencies` | Agencies seen on the server. | `agency` |
| E05 | `/v1/agencies/{agency_id}` | One agency: `external_agency_id`, `current_name`. | `agency` |
| E06 | `/v1/servers/{server_id}/vehicle-position-endpoints` | Feed URLs used by the server, including ones no longer configured. | `vehicle_position_endpoint` |
| E07 | `/v1/vehicle-position-endpoints/{endpoint_id}` | One feed URL, with credentials already removed. | `vehicle_position_endpoint` |

### 4.3 Unmatched stops and trips

| ID | Endpoint | Description | Source tables |
|---|---|---|---|
| E08 | `/v1/reported-stops` | Looks up a reported stop. Requires `agency_id` and `stop_id`. | `reported_stop` |
| E09 | `/v1/reported-stops/{reported_stop_id}` | One reported stop identity. | `reported_stop` |
| E10 | `/v1/reported-stops/{reported_stop_id}/unmatched-stop-episodes` | Every retained episode for the stop: its recurrence history. Accepts `status`, `active_from`, `active_to`. | `unmatched_stop_episode` |
| E11 | `/v1/reported-trips` | Looks up a reported trip. Requires `agency_id` and `trip_id`. | `reported_trip` |
| E12 | `/v1/reported-trips/{reported_trip_id}` | One reported trip identity. | `reported_trip` |
| E13 | `/v1/reported-trips/{reported_trip_id}/unmatched-trip-episodes` | Every retained episode for the trip. Accepts `status`, `active_from`, `active_to`. | `unmatched_trip_episode` |
| E14 | `/v1/unmatched-stop-episodes` | Unmatched-stop episodes. Filters below. | `unmatched_stop_episode`, opening and closing `onebusaway_metrics_response(_agency)` |
| E15 | `/v1/unmatched-stop-episodes/{episode_id}` | One episode: boundaries, `last_reported_at`, `close_reason`, links. | as E14 |
| E16 | `/v1/unmatched-stop-episodes/{episode_id}/static-contexts` | Static context items in time order: `static_stop_found`, stop name, coordinates, parent station, location type, `applies_from`, `applies_until`. | `unmatched_stop_episode_metadata`, `stop_metadata_version`, `gtfs_static_version` |
| E17 | `/v1/unmatched-trip-episodes` | Unmatched-trip episodes. Filters below. | `unmatched_trip_episode`, opening and closing `onebusaway_metrics_response(_agency)` |
| E18 | `/v1/unmatched-trip-episodes/{episode_id}` | One episode. | as E17 |
| E19 | `/v1/unmatched-trip-episodes/{episode_id}/static-contexts` | Static context items in time order: `static_trip_found`, `route_id`, `service_id`, `block_id`, `applies_from`, `applies_until`. | `unmatched_trip_episode_metadata`, `trip_metadata_version`, `gtfs_static_version` |
| E20 | `/v1/oba-metrics-responses/{response_id}` | A stored OBA metrics response: `observed_at`, `upstream_current_time`, `api_version`, `source_implementation`, `source_implementation_version`, and per-agency rows with `response_complete` and the four counts. | `onebusaway_metrics_response(_agency)` |

**Filters for E14 and E17:**
- `server_id`, `agency_id`, `status=open|closed`, `active_from`, `active_to`, `close_reason`.
- `stop_id` (E14) or `trip_id` (E17), which require `agency_id`.
- `source_implementation=java|maglev|unknown`, read from the opening response.
- `static_found=true|false`, matching the episode's latest static context.

**`applies_from` and `applies_until` (E16, E19)** follow the schema document's ordering rule:
- `applies_from` is the later of the version's `published_at` and the episode's `opened_at`.
- `applies_until` is the next item's `applies_from`. For the last item, it is the episode's `closed_at`, or `null` while the episode is open.

### 4.4 Vehicle anomalies

| ID | Endpoint | Description | Source tables |
|---|---|---|---|
| E21 | `/v1/vehicles` | Looks up a vehicle by its feed vehicle ID. Requires `vehicle_id` and either `endpoint_id` or `server_id`. | `vehicle_entity` |
| E22 | `/v1/vehicles/{vehicle_entity_id}` | One vehicle identity, linked to its endpoint. | `vehicle_entity` |
| E23 | `/v1/vehicles/{vehicle_entity_id}/anomaly-episodes` | Every retained anomaly episode for the vehicle. Accepts `anomaly_type`, `status`, `active_from`, `active_to`. | `vehicle_anomaly_episode` |
| E24 | `/v1/vehicle-anomaly-episodes` | Vehicle anomaly episodes. Filters below. | `vehicle_anomaly_episode`, opening and closing `vehicle_position_response` |
| E25 | `/v1/vehicle-anomaly-episodes/{episode_id}` | One episode: type, rule version, `opened_at`, `closed_at`, `close_reason`. | as E24 |
| E26 | `/v1/vehicle-anomaly-episodes/{episode_id}/evidence` | Opening, meaningful-change and resolution evidence in time order, each with its `evidence_role`. | `vehicle_anomaly_episode_evidence`, `vehicle_position_evidence` |
| E27 | `/v1/vehicle-position-evidence/{evidence_id}` | One evidence record with every stored value. | `vehicle_position_evidence` |
| E28 | `/v1/vehicle-position-responses/{response_id}` | A stored feed response: `observed_at`, `source_header_time`, `incrementality`, `payload_sha256`. | `vehicle_position_response` |
| E29 | `/v1/bounding-box-versions/{bounding_box_version_id}` | The four limits actually applied, with scope (server-wide or agency) and static version. | `bounding_box_version` |
| E30 | `/v1/vehicle-anomaly-types` | Anomaly types: `stale_source_timestamp`, `invalid_coordinate`, `stopped_out_of_bounds`. | `vehicle_anomaly_type` |
| E31 | `/v1/vehicle-anomaly-types/{anomaly_code}` | One type. The path uses the stable code instead of the numeric ID. | `vehicle_anomaly_type` |
| E32 | `/v1/vehicle-anomaly-types/{anomaly_code}/rule-versions` | That type's rule history, newest `effective_at` first. | `vehicle_anomaly_rule_version` |
| E33 | `/v1/vehicle-anomaly-rule-versions/{rule_version_id}` | One rule version: `rule_logic_version`, thresholds, `effective_at`, `watchdog_build_version`. | `vehicle_anomaly_rule_version` |

**Filters for E24:**
- `server_id`, `agency_id`, `endpoint_id`, and `vehicle_id` (which requires `endpoint_id` or `server_id`).
- `anomaly_type` (a code), `status`, `close_reason`, `active_from`, `active_to`.
- `agency_id` matches the agency attributed in the episode's opening evidence.

### 4.5 Static GTFS

| ID | Endpoint | Description | Source tables |
|---|---|---|---|
| E34 | `/v1/gtfs-static-versions` | Static versions Watchdog published, newest first. Accepts `server_id`, `published_from`, `published_to`. | `gtfs_static_version` |
| E35 | `/v1/gtfs-static-versions/{gtfs_static_version_id}` | One version: `published_at`, `combined_content_sha256`, `parser_version`, its sources with `source_content_sha256`, and the agencies it covers. | `gtfs_static_version(_source/_agency)`, `static_gtfs_source` |
| E36 | `/v1/static-gtfs-sources/{source_id}` | One static source URL. | `static_gtfs_source` |
| E37 | `/v1/static-gtfs-sources/{source_id}/versions` | The versions that included this source, with that source's hash in each, newest first. A hash that differs from the next item's shows the content changed. | `gtfs_static_version_source` |

### 4.6 Summaries and clusters

These endpoints are computed from stored rows on request. They are read-only queries, not controllers, because they have no side effects.

| ID | Endpoint | Description | Computation |
|---|---|---|---|
| E38 | `/v1/unmatched-stop-clusters` | Requires `server_id` and `at`; accepts `agency_id` and `scheme_version` (default: the newest scheme). Returns unmatched stops open at `at`, grouped by root station and S2 cell. Each item has the cell (`cell_id`, level, centre), the root station (name and ID when known), `open_episode_count`, and links to those episodes. Stops with no static context or assignment are grouped under `unassigned`. | Open episodes at `at` → the static context applying at `at` → `stop_cluster_assignment` for the scheme → `s2_cell` |
| E39 | `/v1/cluster-schemes` | Clustering rule sets. | `cluster_scheme` |
| E40 | `/v1/cluster-schemes/{scheme_version}` | One rule set: S2 level, station grouping, description. | `cluster_scheme` |
| E41 | `/v1/episode-counts` | Requires `from` and `to`; accepts `server_id` and `agency_id`. For `[from, to)`, returns per episode type (`unmatched_stop`, `unmatched_trip`, and each vehicle anomaly code) the number `opened`, `closed` by `close_reason`, and `open_at_to`. | Counts over the episode tables and their opening and closing responses |

---

## 5. Example representations

Fields marked † are **derived** by the API rather than stored as a column:
- `status`: open or closed, from whether a closing response exists.
- `opened_at` / `closed_at`: the opening and closing responses' `observed_at`.
- `duration_seconds`: `closed_at - opened_at`.
- `applies_from` / `applies_until`: see [Section 4.3](#43-unmatched-stops-and-trips).
- `source`: a constant.

All other fields are columns in `00001_create_investigation_schema.sql`. Three fields use API names for columns in related tables: `stop_id`/`trip_id` are `reported_stop.oba_stop_id`/`reported_trip.oba_trip_id`, `vehicle_id` is `vehicle_entity.external_vehicle_id`, and `anomaly_type` is `vehicle_anomaly_type.anomaly_code`. The query filters of the same names match these fields.

### A closed unmatched-stop episode

`GET /v1/unmatched-stop-episodes/4812`

```json
{
  "unmatched_stop_episode_id": 4812,
  "status": "closed",
  "stop_id": "1_75403",
  "agency": { "agency_id": 3, "external_agency_id": "1", "current_name": "King County Metro" },
  "server_id": 1,
  "opened_at": "2026-10-23T01:00:12Z",
  "last_reported_at": "2026-10-23T05:00:02Z",
  "closed_at": "2026-10-23T05:00:32Z",
  "duration_seconds": 14420,
  "close_reason": "not_reported",
  "source_implementation": "maglev",
  "links": {
    "self": "/v1/unmatched-stop-episodes/4812",
    "reported_stop": "/v1/reported-stops/208",
    "agency": "/v1/agencies/3",
    "server": "/v1/servers/1",
    "opening_response": "/v1/oba-metrics-responses/90211",
    "closing_response": "/v1/oba-metrics-responses/90689",
    "static_contexts": "/v1/unmatched-stop-episodes/4812/static-contexts"
  }
}
```

`source_implementation` comes from the opening response. The response carries `Cache-Control: private, max-age=86400` and a strong `ETag`, because a closed episode never changes.

### Its static contexts (GTFS changed while it was open)

`GET /v1/unmatched-stop-episodes/4812/static-contexts`

```json
{
  "items": [
    {
      "stop_metadata_version_id": 7001,
      "source": "watchdog_static_gtfs",
      "static_stop_found": false,
      "stop_name": null,
      "latitude": null,
      "longitude": null,
      "parent_station_id": null,
      "location_type": null,
      "applies_from": "2026-10-23T01:00:12Z",
      "applies_until": "2026-10-23T03:00:00Z",
      "links": { "gtfs_static_version": "/v1/gtfs-static-versions/12" }
    },
    {
      "stop_metadata_version_id": 7140,
      "source": "watchdog_static_gtfs",
      "static_stop_found": true,
      "stop_name": "Main St & 5th Ave",
      "latitude": 47.6105,
      "longitude": -122.3348,
      "parent_station_id": null,
      "location_type": 0,
      "applies_from": "2026-10-23T03:00:00Z",
      "applies_until": "2026-10-23T05:00:32Z",
      "links": { "gtfs_static_version": "/v1/gtfs-static-versions/13" }
    }
  ],
  "links": { "self": "/v1/unmatched-stop-episodes/4812/static-contexts", "episode": "/v1/unmatched-stop-episodes/4812" }
}
```

This is the "OBA had not reloaded yet" case: Watchdog's version 13 contained the stop from 03:00, but OBA kept reporting it until 05:00.

### A paginated episode list

`GET /v1/unmatched-stop-episodes?agency_id=3&active_from=2026-10-23T00:00:00Z&active_to=2026-10-24T00:00:00Z&page_size=2`

```json
{
  "items": [
    { "unmatched_stop_episode_id": 4815, "status": "open", "stop_id": "1_10020", "opened_at": "2026-10-23T22:41:02Z", "last_reported_at": "2026-10-24T09:12:31Z", "closed_at": null, "close_reason": null, "links": { "self": "/v1/unmatched-stop-episodes/4815" } },
    { "unmatched_stop_episode_id": 4812, "status": "closed", "stop_id": "1_75403", "opened_at": "2026-10-23T01:00:12Z", "last_reported_at": "2026-10-23T05:00:02Z", "closed_at": "2026-10-23T05:00:32Z", "close_reason": "not_reported", "links": { "self": "/v1/unmatched-stop-episodes/4812" } }
  ],
  "links": {
    "self": "/v1/unmatched-stop-episodes?agency_id=3&active_from=2026-10-23T00:00:00Z&active_to=2026-10-24T00:00:00Z&page_size=2",
    "next": "/v1/unmatched-stop-episodes?agency_id=3&active_from=2026-10-23T00:00:00Z&active_to=2026-10-24T00:00:00Z&page_size=2&cursor=eyJpZCI6NDgxMn0"
  }
}
```

### A vehicle stale-timestamp episode and its evidence

`GET /v1/vehicle-anomaly-episodes/311`

```json
{
  "vehicle_anomaly_episode_id": 311,
  "status": "closed",
  "anomaly_type": "stale_source_timestamp",
  "vehicle_id": "4021",
  "opened_at": "2026-10-24T15:10:30Z",
  "closed_at": "2026-10-24T15:41:00Z",
  "duration_seconds": 1830,
  "close_reason": "not_present_in_full_dataset",
  "links": {
    "self": "/v1/vehicle-anomaly-episodes/311",
    "vehicle": "/v1/vehicles/77",
    "endpoint": "/v1/vehicle-position-endpoints/2",
    "rule_version": "/v1/vehicle-anomaly-rule-versions/5",
    "opening_response": "/v1/vehicle-position-responses/551203",
    "closing_response": "/v1/vehicle-position-responses/551264",
    "evidence": "/v1/vehicle-anomaly-episodes/311/evidence"
  }
}
```

`GET /v1/vehicle-anomaly-episodes/311/evidence`

```json
{
  "items": [
    {
      "vehicle_position_evidence_id": 98001,
      "evidence_role": "opening",
      "observed_at": "2026-10-24T15:10:30Z",
      "incrementality": "FULL_DATASET",
      "feed_entity_id": "veh-4021",
      "agency_id": 3,
      "vehicle_source_time": "2026-10-24T15:07:55Z",
      "latitude": 47.6062,
      "longitude": -122.3321,
      "position_valid": true,
      "position_validation_reason": null,
      "reported_speed_mps": 0.0,
      "computed_speed_mps": null,
      "speed_discrepancy_ratio": null,
      "current_status": 1,
      "trip_id": "1_604815",
      "route_id": "1_100259",
      "trip_start_date": "2026-10-24",
      "trip_start_time_seconds": 26100,
      "schedule_relationship": "SCHEDULED",
      "expected_schedule_state": "expected_inactive",
      "expected_schedule_reason": "after_scheduled_window",
      "expected_window_start": "2026-10-24T14:15:00Z",
      "expected_window_end": "2026-10-24T15:02:00Z",
      "links": {
        "self": "/v1/vehicle-position-evidence/98001",
        "response": "/v1/vehicle-position-responses/551203",
        "gtfs_static_version": "/v1/gtfs-static-versions/13"
      }
    }
  ],
  "links": { "self": "/v1/vehicle-anomaly-episodes/311/evidence", "episode": "/v1/vehicle-anomaly-episodes/311" }
}
```

`observed_at` and `incrementality` are embedded from the evidence's response, so clients do not need a request per item. This example illustrates UC-06. The agency is on Pacific time (UTC−7), so the trip's scheduled window was 07:15–08:02 local (`trip_start_time_seconds` 26100 is 07:15). The reported time, 08:10 local, is after the trip ended; the feed kept a stale entry, and the episode closed when the vehicle left a complete feed. The evidence does not indicate a vehicle failure.

### Episode counts

`GET /v1/episode-counts?agency_id=3&from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z`

```json
{
  "from": "2026-10-17T00:00:00Z",
  "to": "2026-10-24T00:00:00Z",
  "items": [
    { "episode_type": "unmatched_stop", "opened": 41, "closed": { "not_reported": 38 }, "open_at_to": 3 },
    { "episode_type": "unmatched_trip", "opened": 112, "closed": { "not_reported": 110 }, "open_at_to": 2 },
    { "episode_type": "stale_source_timestamp", "opened": 260, "closed": { "fresh_source_timestamp": 201, "not_present_in_full_dataset": 57 }, "open_at_to": 2 },
    { "episode_type": "invalid_coordinate", "opened": 4, "closed": { "coordinates_valid_again": 4 }, "open_at_to": 0 },
    { "episode_type": "stopped_out_of_bounds", "opened": 9, "closed": { "inside_bounding_box": 6, "no_longer_stopped_at_stop": 3 }, "open_at_to": 0 }
  ],
  "links": { "self": "/v1/episode-counts?agency_id=3&from=2026-10-17T00:00:00Z&to=2026-10-24T00:00:00Z" }
}
```

---

## 6. Schema follow-ups for the API

The main list queries filter by agency or anomaly type and page by episode ID descending. Migration `00001` does not index those paths, so the implementation adds migration `00002` with:

- `unmatched_stop_episode (agency_id, unmatched_stop_episode_id DESC)` and the same for `unmatched_trip_episode`.
- `vehicle_anomaly_episode (anomaly_type_id, vehicle_anomaly_episode_id DESC)`.
- `vehicle_position_evidence (agency_id, vehicle_position_response_id)`. A vehicle episode has no agency column; its agency comes from the opening evidence.

The time-window filter joins each episode to its opening and closing responses' `observed_at`. Both are reached through indexed foreign keys. If profiling shows this join dominates, a later migration can store `opened_at` on the episode rows.

---

## 7. Out of scope

- **Implementation.** Handlers, sqlc queries, auth middleware, route registration and tests come in a later change. Handlers follow the Service and private-function pattern in `CLAUDE.md` and reach the database through `Application.InvestigationDB`.
- **Per-token scopes and `403`.** Add these only if a client other than OBACloud needs restricted access.
- **A combined cross-type timeline.** Typed collections cover every question above.
- **Write endpoints.** Watchdog is the only writer of investigation data.
- **Schema versioning beyond the `/v1` path.** A breaking change to a representation would ship under `/v2`.
