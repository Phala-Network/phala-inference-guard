#ifndef PIG_TOKENIZER_H
#define PIG_TOKENIZER_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct PigTokenizerCountHandle PigTokenizerCountHandle;

enum {
  PIG_TOKENIZER_OK = 0,
  PIG_TOKENIZER_INVALID_ARGUMENT = 1,
  PIG_TOKENIZER_LOAD_ERROR = 2,
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

uint32_t pig_tokenizer_abi_version(void);

#ifdef __cplusplus
}
#endif

#endif
