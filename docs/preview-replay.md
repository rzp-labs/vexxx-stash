# Failed GPU preview replay context

GPU scene MP4 preview failures now carry a versioned `preview_replay` snapshot
through generation jobs and scan/watcher reporting. This supplies correlation
for future failures. It cannot recover already redacted historical requests or
establish the causes of VEX100/VEX102.

## Owner lookup

The persistent location is `preview-replay` inside the directory containing the
active configuration file, on the existing config volume. Preserve this volume
when replacing a container. Generated assets and temporary directories are not
used for retention. No database, public endpoint, or library scan is added.

Copy `preview_replay.reference` from the exception. Read only `current.jsonl`
and `previous.jsonl` in that directory. A matching record contains local-only
`scene_id`, `file_id`, the existing job correlation and the captured snapshot.
These IDs identify the actual primary file selected by the preview task; they
are never telemetry properties. `source_resolved:false` means an ID was missing,
so there is no complete source mapping to claim. Resolve an identified file
through existing local database/API tools before proposing a bounded replay.
Deleted/moved/replaced media may no longer reproduce the original input bytes.

For an owner with local shell access and jq, this read-only lookup tolerates
malformed lines without executing their contents:

```sh
REPLAY_REF='<execution UUID copied from the exception>'
jq -Rrc --arg reference "$REPLAY_REF" \
  'fromjson? | select(.replay.reference == $reference)' \
  '<config directory>/preview-replay/current.jsonl' \
  '<config directory>/preview-replay/previous.jsonl'
```

The Go `diagnostics.FindPreviewReplay` helper additionally enforces file/read
bounds and private namespace checks. It distinguishes a missing reference from
a lookup obstructed by corruption or invalid storage. It opens storage afresh
on every lookup, so a retained mapping does not depend on process memory.

## Capacity, durability and cost

Two JSONL files are capped at 512KiB each (1MiB total); one record is capped at
16KiB including its newline. The writer rotates before exceeding the current
file's limit and replaces the previous file on the next rotation. This is a
space bound, not a time guarantee: old UUIDs expire as failures accumulate.
There is no automatic upload of these local records.

The dedicated directory is created with mode 0700 and files with mode 0600.
Existing broader permissions, symlinks, nonregular files and oversized files
are refused. The existing config parent is not chmodded or recreated. Writes
are serialized and append one record; there is no full-file rewrite or index.
Interrupted final lines are separated from subsequent records. Lookup remains
bounded and can resolve a later valid record despite preceding corrupt lines.

Generation does not wait for disk writes. Failures enqueue to one process-wide
worker with 32 pending records (at most 512KiB of record data by the record
bound). A full queue immediately refuses a new record. Successful generation
performs no persistence IO and starts no retention worker. The worker fsyncs
each successful append. Local lookup reads at most 512KiB per file with bounded
buffer/parser overhead and does no media work.

Telemetry reports `retention:"queued"`, `retained:false` after queue acceptance.
This is not a durability acknowledgement: process termination can lose pending
records, storage can fail, or subsequent rotation can expire them. Only a found
local record reports `retention:"retained"`, `retained:true`. A full/missing sink
reports `unavailable`. Asynchronous write failures emit a fixed warning limited
to once per minute; they never replace the generation error. A filesystem stall
can occupy the single worker and fill its queue, but cannot make generation
wait for retention.

## Reading execution facts

`requested_parameters` stores copied typed options separately from `command`.
Exclusions retain seconds/percent units; requested segment duration remains
distinct from the minimum 0.75s used by a rendered segment. Limit/start/end
values are request options, not inferred command seeks.

Header/admission failures leave seek/duration unset. First-frame metadata reads
from the beginning with a one-frame limit and leaves the selected stream unknown
until metadata succeeds. Decode/filter/encode probes retain their actual mapped
stream, one-frame limit and builder seek. Render failures retain their actual
per-segment seek, positive duration, audio and vsync settings. Render selection
is automatic: the command stream is unset; `plan_input_pixels.metadata_stream_index`
records the independently observed selected source. Concat has no source seek
or processing filter; output validation selects the first output video by `v:0`
and leaves its absolute stream index unknown. `video_selection` distinguishes
automatic selection, an explicit mapped index, and this first-video ordinal.

`started` records the runner's successful cmd.Start when available; unset means
unknown, and false means the command did not start. `filter_configured` describes the supplied filter arguments, not proof that a
frame traversed them. It distinguishes
filter-bearing commands from decode/device/concat/output stages. The canonical
plan ID `intel-scene-preview-v1` identifies the current GPU-only builder (640px
output, High/level 4.2 H264 VAAPI/QP 21, optional AAC 128k, canonical HDR/VR/rotation/
SAR processing). Its exact filter hash, physical input and processed pixel
parameters, supported projection enum and existing runtime fingerprint are
captured without exporting the graph or argv. Use the recorded application
revision as well as the plan ID to reconstruct that builder. Source demuxer
`start_time` is never substituted for a request seek.

Nil values mean unavailable; nonfinite numbers and arbitrary strings are
discarded. Names, paths, media titles, credentials and argv have no record
fields. Telemetry serializes only the safe snapshot, and its existing text
privacy policy remains active. Joined failures retain their own leaf snapshots;
an unrelated sibling never inherits another command's facts. Existing error
identity, cancellation omission and accepted-event deduplication are preserved.

These records are diagnostic evidence. They do not authorize hardware jobs,
change GPU concurrency limits, introduce CPU fallback, or prove a GPU root cause.
