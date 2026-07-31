use std::{error::Error, fmt, path::Path};

use sha2::{Digest, Sha256};
use tokenizers::Tokenizer;

const BLOCK_DIGEST_DOMAIN: &[u8] = b"pig-kv-token-block-v1";
const FULL_BLOCK_TAG: u8 = 1;
const PARTIAL_BLOCK_TAG: u8 = 2;

#[derive(Debug)]
pub struct NativeTokenizer {
    tokenizer: Tokenizer,
}

#[derive(Debug)]
pub struct NativeTokenizerError {
    message: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BlockDigestContext {
    manifest_id: String,
    backend_epoch: String,
    block_size: usize,
    key: [u8; 32],
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PartialBlockMetadata {
    pub token_count: usize,
    pub digest: [u8; 32],
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TokenizationAnalysis {
    pub input_sha256: [u8; 32],
    pub token_count: usize,
    pub full_block_digests: Vec<[u8; 32]>,
    pub partial_block: Option<PartialBlockMetadata>,
    pub token_ids: Option<Vec<u32>>,
}

impl BlockDigestContext {
    pub fn new(
        manifest_id: impl Into<String>,
        backend_epoch: impl Into<String>,
        block_size: usize,
        key: &[u8],
    ) -> Result<Self, NativeTokenizerError> {
        let manifest_id = manifest_id.into();
        if manifest_id.is_empty() {
            return Err(NativeTokenizerError::new(
                "block digest manifest id is required".to_string(),
            ));
        }
        let backend_epoch = backend_epoch.into();
        if backend_epoch.is_empty() {
            return Err(NativeTokenizerError::new(
                "block digest backend epoch is required".to_string(),
            ));
        }
        if block_size == 0 {
            return Err(NativeTokenizerError::new(
                "block digest block size must be positive".to_string(),
            ));
        }
        if key.len() != 32 {
            return Err(NativeTokenizerError::new(
                "block digest key must contain exactly 32 bytes".to_string(),
            ));
        }
        let mut owned_key = [0_u8; 32];
        owned_key.copy_from_slice(key);
        Ok(Self {
            manifest_id,
            backend_epoch,
            block_size,
            key: owned_key,
        })
    }

    pub fn block_size(&self) -> usize {
        self.block_size
    }
}

impl NativeTokenizer {
    pub fn from_file(path: impl AsRef<Path>) -> Result<Self, NativeTokenizerError> {
        let tokenizer = Tokenizer::from_file(path.as_ref())
            .map_err(|error| NativeTokenizerError::new(format!("load tokenizer: {error}")))?;
        Ok(Self::from_tokenizer(tokenizer))
    }

    pub fn from_tokenizer(tokenizer: Tokenizer) -> Self {
        Self { tokenizer }
    }

    pub fn encode(
        &self,
        input: &str,
        add_special_tokens: bool,
    ) -> Result<Vec<u32>, NativeTokenizerError> {
        let encoding = self
            .tokenizer
            .encode(input, add_special_tokens)
            .map_err(|error| NativeTokenizerError::new(format!("encode input: {error}")))?;
        Ok(encoding.get_ids().to_vec())
    }

    pub fn analyze(
        &self,
        input: &str,
        add_special_tokens: bool,
        context: &BlockDigestContext,
        retain_token_ids: bool,
    ) -> Result<TokenizationAnalysis, NativeTokenizerError> {
        let encoding = self
            .tokenizer
            .encode(input, add_special_tokens)
            .map_err(|error| NativeTokenizerError::new(format!("encode input: {error}")))?;
        let token_ids = encoding.get_ids();
        let mut full_block_digests = Vec::with_capacity(token_ids.len() / context.block_size);
        let mut digest_stream = block_digest_stream(context);
        let mut chunks = token_ids.chunks_exact(context.block_size);
        for (block_index, block) in (&mut chunks).enumerate() {
            update_digest_block(&mut digest_stream, FULL_BLOCK_TAG, block_index, block);
            full_block_digests.push(*digest_stream.clone().finalize().as_bytes());
        }
        let remainder = chunks.remainder();
        let partial_block = if remainder.is_empty() {
            None
        } else {
            update_digest_block(
                &mut digest_stream,
                PARTIAL_BLOCK_TAG,
                full_block_digests.len(),
                remainder,
            );
            Some(PartialBlockMetadata {
                token_count: remainder.len(),
                digest: *digest_stream.finalize().as_bytes(),
            })
        };
        let input_sha256: [u8; 32] = Sha256::digest(input.as_bytes()).into();
        Ok(TokenizationAnalysis {
            input_sha256,
            token_count: token_ids.len(),
            full_block_digests,
            partial_block,
            token_ids: retain_token_ids.then(|| token_ids.to_vec()),
        })
    }
}

fn block_digest_stream(context: &BlockDigestContext) -> blake3::Hasher {
    let mut digest = blake3::Hasher::new_keyed(&context.key);
    digest.update(BLOCK_DIGEST_DOMAIN);
    update_length_prefixed(&mut digest, context.manifest_id.as_bytes());
    update_length_prefixed(&mut digest, context.backend_epoch.as_bytes());
    digest.update(&(context.block_size as u64).to_le_bytes());
    digest
}

fn update_digest_block(
    digest: &mut blake3::Hasher,
    block_tag: u8,
    block_index: usize,
    token_ids: &[u32],
) {
    digest.update(&[block_tag]);
    digest.update(&(block_index as u64).to_le_bytes());
    digest.update(&(token_ids.len() as u64).to_le_bytes());
    for token_id in token_ids {
        digest.update(&u64::from(*token_id).to_le_bytes());
    }
}

fn update_length_prefixed(digest: &mut blake3::Hasher, value: &[u8]) {
    digest.update(&(value.len() as u64).to_le_bytes());
    digest.update(value);
}

impl NativeTokenizerError {
    fn new(message: String) -> Self {
        Self { message }
    }
}

impl fmt::Display for NativeTokenizerError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl Error for NativeTokenizerError {}

#[cfg(test)]
mod tests {
    use ahash::AHashMap;
    use tokenizers::{
        Tokenizer, models::wordlevel::WordLevel, pre_tokenizers::whitespace::Whitespace,
    };

    use super::NativeTokenizer;

    #[test]
    fn returns_exact_ids_from_the_loaded_tokenizer() {
        let vocabulary = AHashMap::from([
            ("[UNK]".to_string(), 0),
            ("hello".to_string(), 1),
            ("world".to_string(), 2),
        ]);
        let model = WordLevel::builder()
            .vocab(vocabulary)
            .unk_token("[UNK]".to_string())
            .build()
            .expect("word-level model");
        let mut tokenizer = Tokenizer::new(model);
        tokenizer.with_pre_tokenizer(Some(Whitespace));

        let native = NativeTokenizer::from_tokenizer(tokenizer);
        let token_ids = native.encode("hello world", false).expect("encode");

        assert_eq!(token_ids, vec![1, 2]);
    }

    #[test]
    fn invalid_tokenizer_file_is_rejected() {
        let error = NativeTokenizer::from_file("/does/not/exist/tokenizer.json")
            .expect_err("missing tokenizer must fail");
        assert!(error.to_string().contains("load tokenizer"));
    }
}
