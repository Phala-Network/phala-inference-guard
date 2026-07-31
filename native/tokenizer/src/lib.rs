use std::{error::Error, fmt, path::Path};

use hmac::{Hmac, Mac};
use sha2::{Digest, Sha256};
use tokenizers::Tokenizer;

type HmacSha256 = Hmac<Sha256>;

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
    key: Vec<u8>,
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
        if key.len() < 16 {
            return Err(NativeTokenizerError::new(
                "block digest key must contain at least 16 bytes".to_string(),
            ));
        }
        Ok(Self {
            manifest_id,
            backend_epoch,
            block_size,
            key: key.to_vec(),
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
        let mut previous = [0_u8; 32];
        let mut chunks = token_ids.chunks_exact(context.block_size);
        for block in &mut chunks {
            let digest = block_digest(context, FULL_BLOCK_TAG, &previous, block)?;
            full_block_digests.push(digest);
            previous = digest;
        }
        let remainder = chunks.remainder();
        let partial_block = if remainder.is_empty() {
            None
        } else {
            Some(PartialBlockMetadata {
                token_count: remainder.len(),
                digest: block_digest(context, PARTIAL_BLOCK_TAG, &previous, remainder)?,
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

fn block_digest(
    context: &BlockDigestContext,
    block_tag: u8,
    previous: &[u8; 32],
    token_ids: &[u32],
) -> Result<[u8; 32], NativeTokenizerError> {
    let mut digest = HmacSha256::new_from_slice(&context.key)
        .map_err(|error| NativeTokenizerError::new(format!("initialize block digest: {error}")))?;
    digest.update(BLOCK_DIGEST_DOMAIN);
    update_length_prefixed(&mut digest, context.manifest_id.as_bytes());
    update_length_prefixed(&mut digest, context.backend_epoch.as_bytes());
    digest.update(&(context.block_size as u64).to_le_bytes());
    digest.update(previous);
    digest.update(&[block_tag]);
    digest.update(&(token_ids.len() as u64).to_le_bytes());
    for token_id in token_ids {
        digest.update(&u64::from(*token_id).to_le_bytes());
    }
    let bytes = digest.finalize().into_bytes();
    let mut output = [0_u8; 32];
    output.copy_from_slice(&bytes);
    Ok(output)
}

fn update_length_prefixed(digest: &mut HmacSha256, value: &[u8]) {
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
