<script lang="ts">
  // Images a message carried, small, each opening full size in a new tab.
  let { images }: { images: { n: number; src: string }[] } = $props();
</script>

<div class="pasted">
  {#each images as image (image.n)}
    <a href={image.src} target="_blank" rel="noopener" title="[Image #{image.n}]">
      <img src={image.src} alt="[Image #{image.n}]" loading="lazy" />
      <span>#{image.n}</span>
    </a>
  {/each}
</div>
