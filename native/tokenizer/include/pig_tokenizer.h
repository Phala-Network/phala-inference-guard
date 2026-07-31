#ifndef PIG_TOKENIZER_H
#define PIG_TOKENIZER_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct PigTokenizerHandle PigTokenizerHandle;
typedef struct PigTokenizerCountHandle PigTokenizerCountHandle;
typedef struct PigTokenizationAnalysisHandle PigTokenizationAnalysisHandle;

typedef struct PigTokenizationAnalysisView {
  uint64_t token_count;
  uint64_t full_block_count;
  const uint8_t *full_block_digests;
  uint64_t partial_block_tokens;
  uint8_t partial_block_digest[32];
} PigTokenizationAnalysisView;

enum {
  PIG_TOKENIZER_OK = 0,
  PIG_TOKENIZER_INVALID_ARGUMENT = 1,
  PIG_TOKENIZER_LOAD_ERROR = 2,
  PIG_TOKENIZER_ANALYSIS_ERROR = 3,
  PIG_TOKENIZER_PANIC = 4,
  PIG_TOKENIZER_COUNT_ERROR = 5
};

int32_t pig_tokenizer_count_open(const char *tokenizer_path,
                                 PigTokenizerCountHandle **out_handle,
                                 char *error_buffer,
                                 size_t error_capacity);

int32_t pig_tokenizer_count(PigTokenizerCountHandle *handle,
                            const uint8_t *input,
                            size_t input_len,
                            uint8_t add_special_tokens,
                            uint64_t *out_token_count,
                            char *error_buffer,
                            size_t error_capacity);

int32_t pig_tokenizer_count_destroy(PigTokenizerCountHandle **handle);

int32_t pig_tokenizer_open(const char *tokenizer_path,
                           const char *manifest_id,
                           const char *backend_epoch,
                           uint64_t block_size,
                           const uint8_t *key,
                           size_t key_len,
                           PigTokenizerHandle **out_handle,
                           char *error_buffer,
                           size_t error_capacity);

int32_t pig_tokenizer_analyze(PigTokenizerHandle *handle,
                              const uint8_t *input,
                              size_t input_len,
                              uint8_t add_special_tokens,
                              PigTokenizationAnalysisHandle **out_analysis,
                              char *error_buffer,
                              size_t error_capacity);

int32_t pig_tokenizer_analysis_view(
    const PigTokenizationAnalysisHandle *analysis,
    PigTokenizationAnalysisView *out_view);

int32_t pig_tokenizer_analysis_destroy(
    PigTokenizationAnalysisHandle **analysis);

int32_t pig_tokenizer_destroy(PigTokenizerHandle **handle);

uint32_t pig_tokenizer_abi_version(void);

#ifdef __cplusplus
}
#endif

#endif
