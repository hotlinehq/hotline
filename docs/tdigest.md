# t-digest under the radix tree

Why `radixtree` demands a commutative monoid from its payload, why t-digest is
not one, and what that costs — including the implementation Redis ships.

## What the tree requires

`radixtree.Tree[T]` is a cache of pre-folded partial results arranged by time.
The only operation it performs on a payload is `Merge`, and `Factory[T]`
supplies the identity element. That is the whole contract in
[`mergeable.go`](../src/hotline/metrics/radixtree/mergeable.go):

> Anything that is a commutative monoid under Merge can be indexed by it.

The word *commutative* is not decoration. Three things in the tree fold the
same measurements in different groupings and different orders:

**Range decomposition.** `AggregateRange(from, to)` merges one precomputed
aggregate for every block that lies entirely inside the range and descends only
at the two boundaries. Which blocks those are depends on where the window
happens to fall against the radix-16 prefixes. The same second therefore
reaches the result through a different bracketing depending on the window it is
queried in — that is associativity, asked for at every query.

**Partials arriving in any order.** `SlidingWindowSketch.AddPartial` folds a
sketch built elsewhere into a second, and the sharded-ingestion specs merge the
same stream under 2, 3, 5, 8 and 16 shard layouts. Nothing coordinates arrival
order — that is commutativity.

**Correction and unwind.** `Update` recomputes every node aggregate on the root
to leaf path from its children rather than patching it, which is what lets the
tree carry a non-invertible operator at all. A late event re-folds its leaf's
ancestors, so those merges happen more than once over the same data.

The tree does what it can on its own: `node.fold` iterates children in fixed
slot order, so a single node's fold is reproducible even for an order-dependent
payload. It cannot fix the grouping, because the grouping is chosen by the
query.

## The laws, in plain terms

Three symbols carry the rest of this page.

**A, B, C** are digests: each one summarises a set of latency
measurements. In this codebase a digest is usually one second of traffic — the
leaf the tree stores — or one shard's partial result for a second.

**⊕ is merge.** `A ⊕ B` is a digest summarising everything in A together with
everything in B. In Go that is `a.Merge(b)`; in Redis it is `TDIGEST.MERGE`.

**E is the empty digest**, which merges into anything without changing it.

Three properties matter, and they are what "commutative monoid" names:

| law | in symbols | in words |
|---|---|---|
| identity | `E ⊕ A = A` | merging nothing changes nothing |
| associativity | `(A ⊕ B) ⊕ C = A ⊕ (B ⊕ C)` | the **grouping** does not matter |
| commutativity | `A ⊕ B = B ⊕ A` | the **order** does not matter |

### Why a bucket histogram obeys them

A DDSketch is a map from bucket index to a count. Say three seconds of traffic
land in buckets 3 and 9:

```
A = {3: 2}          B = {3: 1}          C = {9: 1}

(A ⊕ B) ⊕ C  =  {3: 3} ⊕ {9: 1}  =  {3: 3, 9: 1}
A ⊕ (B ⊕ C)  =  {3: 2} ⊕ {3: 1, 9: 1}  =  {3: 3, 9: 1}
```

Merge is integer addition per bucket, and addition does not care about grouping
or order, so both give the same answer — and so does any other bracketing the
tree happens to pick.

### Why t-digest does not

A t-digest stores centroids, `(mean, weight)` pairs, and each centroid may only
grow so heavy before it must be left alone — that limit is what keeps the digest
small. Compression fuses neighbouring centroids while the limit allows, and it
runs whenever a merge fills the digest up, not only at the end.

Two things follow. First, **fusing is lossy**: once `(10,1)` and `(12,1)` become
`(11,2)`, the digest no longer knows there was ever a 10 or a 12. Second,
**what gets fused depends on what is present when compression runs** — and that
differs between groupings.

Take three measurements, 10ms, 12ms and 14ms, with a limit of weight 2 per
centroid:

```
A = [(10, 1)]       B = [(12, 1)]       C = [(14, 1)]

(A ⊕ B) ⊕ C:  A⊕B fuses to [(11,2)], which is now at its limit,
              so 14 cannot join it        →  [(11, 2), (14, 1)]

A ⊕ (B ⊕ C):  B⊕C fuses to [(13,2)], which is now at its limit,
              so 10 cannot join it        →  [(10, 1), (13, 2)]
```

Same three measurements, different surviving centroids, and the quantiles read
off one are not the quantiles read off the other. Nothing went wrong: each
merge did exactly what it should with the centroids it had in front of it. The
first fuse simply foreclosed the choice the second grouping still had.

(A limit of 2 stands in for t-digest's scale function, which allows more weight
in the tails than in the middle and reads the total weight as it stands when
compression fires. The mechanism is what matters here: fuse early, and the
alternative grouping is gone.)

That is associativity lost. Under the tree it means the answer depends on how a
window decomposes into blocks — and the blocks are chosen by the query, not by
the caller.

## What the suite already shows

None of this is a suspicion about the algorithm; it is a property the suite
pins. [`digestcompare`](../src/hotline/metrics/digestcompare) replays one
identical stream through both payloads under the same tree and counts how many
distinct results 200 repartitions produce:

- DDSketch: exactly one — *"a repartition MUST NOT change a DDSketch"*
- t-digest: more than one — *"t-digest merge re-clusters, so it cannot be order
  independent"*

## The implementation Redis ships

RedisBloom vendors [RedisBloom/t-digest-c](https://github.com/RedisBloom/t-digest-c),
a fork of the C merging-digest port, behind the `TDIGEST.*` commands. It fails
both laws, and commutativity for a sharper reason than our own port does.

```c
int td_merge(td_histogram_t *into, td_histogram_t *from) {
    if (td_compress(into) != 0) return EDOM;
    if (td_compress(from) != 0) return EDOM;
    const int pos = from->merged_nodes + from->unmerged_nodes;
    for (int i = 0; i < pos; i++) {
        const double mean = from->nodes_mean[i];
        const long long weight = from->nodes_weight[i];
        if (td_add(into, mean, weight) != 0) return EDOM;
    }
    return 0;
}
```

`td_add` compresses first when the node array is nearly full
(`(merged + unmerged) >= cap - 1`), and `td_compress` sorts the combined
centroids by mean before re-clustering greedily under a limit whose
`normalizer` is `compression / (2π · total_weight · log(total_weight))`.

**Associativity fails**, for the reason above plus one more: the boundary test
reads `total_weight` as it stands when compression fires, so `(A ⊕ B) ⊕ C` and
`A ⊕ (B ⊕ C)` cluster against different totals as well as different neighbours.

**Commutativity fails too**, which is the sharper point — `A ⊕ B` and `B ⊕ A`
are not the same operation here. `td_merge(into, from)` pours the source's
centroids into the exporter one at a time while compression triggers on the
exporter's fill, so the two arguments are not interchangeable. The sort is
introsort with three-way partitioning and is not stable — the source notes the
order within a run of equal keys differs from a naive sort — so tied means can
resolve by array layout as well.

An empty digest is still an identity and merge is closed, so what Redis offers
is a magma with identity, not a monoid. `TDIGEST.MERGE` buys accuracy, not
shard invariance.

Read from `master` of the source above rather than measured by running it. The
empirical check, if it is ever worth doing: create three sketches, merge them
in both groupings and both orders, and compare `TDIGEST.BYRANK`.

## What this costs us

- [`ddsketch`](../src/hotline/metrics/ddsketch) is the production payload. Its
  merge is per-bucket integer addition, exactly associative and commutative, so
  one fold serves per-second leaves, internal nodes, cross-process partials and
  cross-shard rollups alike.
- `SlidingWindowSketch` exists only in `ddsketch`. Its contract — out-of-order
  equivalence, identical results under any shard layout, eviction that never
  changes an answer — is exactly what a t-digest payload cannot honour, so a
  `tdigest` counterpart would publish guarantees it would break.
- [`tdigest`](../src/hotline/metrics/tdigest) stays as the control. It is still
  indexed by `radixtree.Tree[*tdigest.Sketch]`, because the tree asks only for
  `Mergeable`; the comparison uses that to quantify the divergence rather than
  assume it.
