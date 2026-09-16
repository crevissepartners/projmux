// A message the composer sent carries each pasted image as the path the
// server stored it at. Shown back, those paths read as `[Image #N]` with the
// image beside the text, the way they were typed.

const STORED = /(?:[^\s"'`<>()]*\/)?web-uploads\/([0-9a-f]{64}\.(?:png|jpg|webp|gif))/g;

export interface WithImages {
  text: string;
  images: { n: number; src: string }[];
}

export function withImages(text: string): WithImages {
  const images: WithImages["images"] = [];
  const replaced = text.replace(STORED, (_whole, name: string) => {
    const n = images.length + 1;
    images.push({ n, src: `/api/v1/web/uploads/${name}` });
    return `[Image #${n}]`;
  });
  return { text: replaced, images };
}
