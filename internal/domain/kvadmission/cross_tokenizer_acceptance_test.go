package kvadmission

import (
	"sort"
	"sync"
	"testing"
)

const v01217ComparisonCommit = "0091241bc9edc30f0f7ff50010504225d3fa14c8"

type v01217ComparisonEstimate struct {
	selection   int64
	reservation int64
}

var v01217ComparisonEstimates = map[string]v01217ComparisonEstimate{
	"chat_escaped_cjk": {
		selection:   17_450,
		reservation: 26_175,
	},
	"chat_metadata_heavy": {
		selection:   16_396,
		reservation: 18_446,
	},
	"chat_tool_schema": {
		selection:   8_071,
		reservation: 13_310,
	},
}

func TestV01218ComparisonCorpusReducesV01217KVOverestimation(t *testing.T) {
	manifest := loadCrossTokenizerOracleManifest(t)
	validateCrossTokenizerOracleManifest(t, manifest)
	candidateOverestimates := make([]int64, 0, len(v01217ComparisonEstimates)*len(manifest.Tokenizers))
	baselineOverestimates := make([]int64, 0, cap(candidateOverestimates))
	compared := 0

	for _, fixture := range manifest.Fixtures {
		if !hasCrossTokenizerOracleTag(fixture.Tags, "comparison") {
			continue
		}
		baseline, exists := v01217ComparisonEstimates[fixture.Name]
		if !exists {
			t.Fatalf("missing %s comparison estimate for baseline %s", fixture.Name, v01217ComparisonCommit)
		}
		candidate := crossTokenizerEstimate(t, fixture).Estimate
		if candidate.SelectionInputTokens >= baseline.selection ||
			candidate.KVReservationInputTokens >= baseline.reservation {
			t.Fatalf(
				"fixture=%s candidate selection/reservation=%d/%d baseline=%d/%d",
				fixture.Name,
				candidate.SelectionInputTokens,
				candidate.KVReservationInputTokens,
				baseline.selection,
				baseline.reservation,
			)
		}
		for _, oracle := range fixture.Oracle {
			candidateOverestimate := candidate.KVReservationInputTokens - oracle.AggregateInputTokens
			baselineOverestimate := baseline.reservation - oracle.AggregateInputTokens
			if candidateOverestimate < 0 || baselineOverestimate < 0 {
				t.Fatalf(
					"fixture=%s family=%s unsafe comparison candidate=%d baseline=%d oracle=%d",
					fixture.Name,
					oracle.Family,
					candidate.KVReservationInputTokens,
					baseline.reservation,
					oracle.AggregateInputTokens,
				)
			}
			candidateOverestimates = append(candidateOverestimates, candidateOverestimate)
			baselineOverestimates = append(baselineOverestimates, baselineOverestimate)
		}
		t.Logf(
			"fixture=%s candidate selection/reservation=%d/%d baseline=%d/%d",
			fixture.Name,
			candidate.SelectionInputTokens,
			candidate.KVReservationInputTokens,
			baseline.selection,
			baseline.reservation,
		)
		compared++
	}

	if compared != len(v01217ComparisonEstimates) {
		t.Fatalf("comparison fixtures=%d want=%d", compared, len(v01217ComparisonEstimates))
	}
	candidateMedian := nearestRank(candidateOverestimates, 50)
	baselineMedian := nearestRank(baselineOverestimates, 50)
	candidateP95 := nearestRank(candidateOverestimates, 95)
	baselineP95 := nearestRank(baselineOverestimates, 95)
	if candidateMedian >= baselineMedian || candidateP95 >= baselineP95 {
		t.Fatalf(
			"KV overestimation did not improve: candidate median/p95=%d/%d baseline=%d/%d",
			candidateMedian,
			candidateP95,
			baselineMedian,
			baselineP95,
		)
	}
	t.Logf(
		"baseline_commit=%s KV_overestimate_tokens candidate median/p95=%d/%d baseline=%d/%d",
		v01217ComparisonCommit,
		candidateMedian,
		candidateP95,
		baselineMedian,
		baselineP95,
	)
}

func TestV01218RepresentativeEstimatorHasBoundedAllocations(t *testing.T) {
	corpus := crossTokenizerBenchmarkCorpus(t)
	allocations := testing.AllocsPerRun(100, func() {
		for _, fixture := range corpus {
			if _, valid := crossTokenizerEstimateBody(fixture.endpoint, fixture.body); !valid {
				t.Fatalf("unsupported representative fixture: %s", fixture.name)
			}
		}
	})
	perRequest := allocations / float64(len(corpus))
	if perRequest > 1 {
		t.Fatalf("representative estimator allocations/request=%g want <=1", perRequest)
	}
	t.Logf("representative estimator allocations/request=%g", perRequest)
}

func TestV01218RepresentativeEstimatorHandlesMixedConcurrency(t *testing.T) {
	corpus := crossTokenizerBenchmarkCorpus(t)
	const workers = 64
	const requestsPerWorker = 32
	start := make(chan struct{})
	errors := make(chan string, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(offset int) {
			defer wait.Done()
			<-start
			for request := 0; request < requestsPerWorker; request++ {
				fixture := corpus[(offset+request)%len(corpus)]
				if _, valid := crossTokenizerEstimateBody(fixture.endpoint, fixture.body); !valid {
					errors <- fixture.name
					return
				}
			}
		}(worker)
	}
	close(start)
	wait.Wait()
	close(errors)
	for fixture := range errors {
		t.Fatalf("unsupported representative fixture under concurrency: %s", fixture)
	}
}

func BenchmarkV01218RepresentativeEndpointEstimator(b *testing.B) {
	corpus := crossTokenizerBenchmarkCorpus(b)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		fixture := corpus[index%len(corpus)]
		cost, valid := crossTokenizerEstimateBody(fixture.endpoint, fixture.body)
		if !valid {
			b.Fatalf("unsupported representative fixture: %s", fixture.name)
		}
		crossTokenizerBenchmarkSink = cost.Estimate.SelectionInputTokens
	}
}

type crossTokenizerBenchmarkFixture struct {
	name     string
	endpoint string
	body     []byte
}

func crossTokenizerBenchmarkCorpus(t testing.TB) []crossTokenizerBenchmarkFixture {
	t.Helper()
	manifest := loadCrossTokenizerOracleManifest(t)
	validateCrossTokenizerOracleManifest(t, manifest)
	corpus := make([]crossTokenizerBenchmarkFixture, 0, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		corpus = append(corpus, crossTokenizerBenchmarkFixture{
			name:     fixture.Name,
			endpoint: fixture.Endpoint,
			body:     crossTokenizerOracleBody(t, fixture),
		})
	}
	return corpus
}

func nearestRank(values []int64, percentile int) int64 {
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	rank := (len(ordered)*percentile + 99) / 100
	return ordered[rank-1]
}

var crossTokenizerBenchmarkSink int64
