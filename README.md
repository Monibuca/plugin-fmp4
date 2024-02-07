# plugin-fmp4
可以通过http访问MP4

# 用法

如果有一个流名为live/test，则可以通过下面的方式访问播放

```shell
ffplay http://localhost:8080/fmp4/live/test.mp4
```

如果在浏览器里面可以直接使用 video 标签播放

```html
<video src="http://localhost:8080/fmp4/live/test.mp4" controls></video>
```